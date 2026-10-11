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

function fakeHelper(on: On, posted: unknown[], isUp = true) {
  on('http.fetch', ($, e) => {
    if (!isUp) {
      return { deny: 'connection refused' }
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

  test('a drag on the overlay reaches the helper as one gesture', async ($, on) => {
    mock.clock(on)
    const posted: unknown[] = []
    fakeHelper(on, posted)
    const ui = await $.ui.mount(PANE)
    await ui.press({ key: 'live' })

    await ui.pointer({ in: 'touch', type: 'down', x: 2, y: 10, button: 'left' })
    await ui.pointer({ in: 'touch', type: 'move', x: 2, y: 5, button: 'left' })
    await ui.pointer({ in: 'touch', type: 'up', x: 2, y: 1, button: 'left' })

    expect(posted).toEqual([
      expect.objectContaining({
        type: 'gesture',
        points: expect.arrayContaining([expect.objectContaining({ t: expect.any(Number) })]),
      }),
    ])
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
