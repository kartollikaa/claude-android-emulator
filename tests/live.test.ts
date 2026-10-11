import { describe, expect, mock, test } from 'claude-code/testing'
import type { On } from 'claude-code'

import { parseLiveFrame } from '../hooks/live'

const PANE = {
  plugin: 'android-emulator',
  surface: 'desktop',
  component: 'Pane',
  requestId: 'android',
  props: {
    title: 'Android',
    isFocused: true,
    bodyColumns: 100,
    placement: 'dock',
    scroll: { offset: 0, bodyRows: 40 },
    view: {},
  },
} as const

const FRAME = { seq: 7, width: 360, height: 800, base64: 'QUJD' }
const FILE_FRAME = { seq: 9, width: 540, height: 1200, path: '/tmp/claude-android-emulator/live/slot-1.rgb' }

function fakeHelper(on: On, posted: unknown[], isUp = true) {
  on('http.fetch', ($, e) => {
    if (!isUp) {
      return { deny: 'connection refused' }
    }
    if (e.url.includes('/frame-file')) {
      return { value: { status: 200, ok: true, headers: {}, text: JSON.stringify(FILE_FRAME) } }
    }
    if (e.url.includes('/frame')) {
      return { value: { status: 200, ok: true, headers: {}, text: JSON.stringify(FRAME) } }
    }
    posted.push(JSON.parse(e.init?.body ?? 'null'))

    return { value: { status: 204, ok: true, headers: {}, text: '' } }
  })
}

describe('parseLiveFrame', () => {
  test('keeps a frame the helper sent and ignores an unchanged answer', () => {
    expect(parseLiveFrame(JSON.stringify(FRAME))).toEqual(FRAME)
    expect(parseLiveFrame('{"seq":7}')).toBeNull()
  })
})

describe('live mode on desktop', () => {
  test('Go live draws the helper frame with a touch overlay over it', async ($, on) => {
    mock.clock(on)
    fakeHelper(on, [])
    const ui = await $.ui.mount(PANE)

    await ui.press({ key: 'live' })

    const picture = await ui.find({ type: 'Svg' })
    expect(JSON.stringify(picture)).toContain('data:image/jpeg;base64,QUJD')
    expect(await ui.find({ key: 'nav-GoHome' })).toBeDefined()
    expect(await ui.find({ key: 'live', text: /Stop live/ })).toBeDefined()
    await ui.press({ key: 'live' })
    await ui.unmount()
  })

  test('a press on an overlay with no size says so instead of tapping a corner', async ($, on) => {
    mock.clock(on)
    const posted: unknown[] = []
    fakeHelper(on, posted)
    const ui = await $.ui.mount(PANE)
    await ui.press({ key: 'live' })

    await ui.pointer({ in: 'touch', type: 'down', x: 2, y: 10, button: 'left' })
    await ui.pointer({ in: 'touch', type: 'up', x: 2, y: 10, button: 'left' })

    expect(posted).toEqual([expect.objectContaining({ type: 'debug', kind: 'pointer', columns: 0 })])
    expect(await ui.find({ type: 'Text', text: /overlay has no size/ })).toBeDefined()
    await ui.press({ key: 'live' })
    await ui.unmount()
  })

  test('a drag on the laid-out overlay reaches the helper as one gesture', async ($, on) => {
    mock.clock(on)
    const posted: Array<Record<string, unknown>> = []
    fakeHelper(on, posted)
    const ui = await $.ui.mount(PANE)
    await ui.press({ key: 'live' })
    await ui.resize({ in: 'touch', columns: 30, rows: 60 })

    await ui.pointer({ in: 'touch', type: 'down', x: 15, y: 50, button: 'left' })
    await ui.pointer({ in: 'touch', type: 'move', x: 15, y: 30, button: 'left' })
    await ui.pointer({ in: 'touch', type: 'up', x: 15, y: 10, button: 'left' })

    expect(posted.map(message => message.type)).toEqual(['debug', 'gesture'])
    const gesture = posted[1] as { points: Array<{ x: number; y: number }> }
    expect(gesture.points[0]).toEqual(expect.objectContaining({ x: 15.5 / 30, y: 50.5 / 60 }))
    await ui.press({ key: 'live' })
    await ui.unmount()
  })

  test('the rulers measure the code-font cell and size the touch overlay to the picture', async ($, on) => {
    mock.clock(on)
    fakeHelper(on, [])
    const ui = await $.ui.mount(PANE)
    await ui.press({ key: 'live' })

    await ui.resize({ in: 'ruler-x', columns: 36, rows: 1 })
    await ui.resize({ in: 'ruler-y', columns: 1, rows: 32 })
    await ui.advance(600)

    const touch = JSON.stringify(await ui.find({ type: 'Client', key: 'touch' }))
    expect(touch).toMatch(/"width":36/)
    expect(touch).toMatch(/"height":32/)
    await ui.press({ key: 'live' })
    await ui.unmount()
  })

  test('the Home button sends the GoHome key', async ($, on) => {
    mock.clock(on)
    const posted: unknown[] = []
    fakeHelper(on, posted)
    const ui = await $.ui.mount(PANE)
    await ui.press({ key: 'live' })

    await ui.press({ key: 'nav-GoHome' })

    expect(posted).toEqual([{ type: 'key', key: 'GoHome' }])
    await ui.press({ key: 'live' })
    await ui.unmount()
  })

  test('an unreachable helper stops live mode and says how to start it', async ($, on) => {
    mock.clock(on)
    fakeHelper(on, [], false)
    const ui = await $.ui.mount(PANE)

    await ui.press({ key: 'live' })

    expect(await ui.find({ type: 'Text', text: /node prototypes\/live-helper\/server\.mjs/ })).toBeDefined()
    expect(await ui.find({ key: 'live', text: /Go live/ })).toBeDefined()
    await ui.unmount()
  })
})

describe('live mode on the terminal', () => {
  test('Go live draws the helper frame file as an Image with the touch overlay', async ($, on) => {
    mock.clock(on)
    fakeHelper(on, [])
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })

    await ui.press({ key: 'live' })

    const screen = await ui.find({ type: 'Image', key: 'screen' })
    expect(JSON.stringify(screen)).toContain('slot-1.rgb')
    expect(await ui.find({ type: 'Client', key: 'touch' })).toBeDefined()
    expect(await ui.find({ key: 'probe' })).toBeUndefined()
    await ui.press({ key: 'live' })
    await ui.unmount()
  })

  test('a drag on the overlay reaches the helper as one gesture in screen fractions', async ($, on) => {
    mock.clock(on)
    const posted: Array<Record<string, unknown>> = []
    fakeHelper(on, posted)
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.press({ key: 'live' })
    await ui.resize({ in: 'touch', columns: 36, rows: 40 })

    await ui.pointer({ in: 'touch', type: 'down', x: 10, y: 30, button: 'left' })
    await ui.pointer({ in: 'touch', type: 'move', x: 10, y: 15, button: 'left' })
    await ui.pointer({ in: 'touch', type: 'up', x: 10, y: 2, button: 'left' })

    const gesture = posted.find(message => message.type === 'gesture') as { points: Array<{ x: number; y: number }> }
    expect(gesture.points).toHaveLength(3)
    expect(gesture.points[0]!.y).toBeGreaterThan(gesture.points[2]!.y)
    expect(gesture.points.every(point => point.x > 0 && point.x < 1 && point.y > 0 && point.y < 1)).toBe(true)
    expect(await ui.find({ type: 'Text', text: /Touch: sent a gesture of 3 points/ })).toBeDefined()
    await ui.press({ key: 'live' })
    await ui.unmount()
  })
})

describe('stream test on desktop', () => {
  test('draws each embedding of the helper stream', async ($, on) => {
    mock.clock(on)
    fakeHelper(on, [])
    const ui = await $.ui.mount(PANE)

    await ui.press({ key: 'probe' })

    const pictures = await ui.findAll({ type: 'Svg' })
    expect(JSON.stringify(pictures)).toContain('stream.mjpeg?v=svg-interactive')
    expect(JSON.stringify(pictures)).toContain('stream.mjpeg?v=svg-plain')
    expect(await ui.find({ type: 'Markdown', text: /stream\.mjpeg\?v=markdown/ })).toBeDefined()
    await ui.unmount()
  })
})
