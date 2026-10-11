import { expect, test } from 'claude-code/testing'
import type { Engine } from 'claude-code/testing'
import type { On } from 'claude-code'

import { runAction, TOOL } from '../hooks/tool'
import type { Call, Reply } from '../hooks/tool'
import { fakeHost, MAC, URL } from './host'

type Request = { method: string; path: string; body?: unknown }
type Answer = Partial<Reply> | ((request: Request) => Partial<Reply>)

const BOOTED = { connected: true, booted: true, avd: 'Pixel_9' }
const STOPPED = { connected: false, booted: false, avd: '' }
const SCREEN = { jpeg: '/9j/4AAQ', width: 576, height: 1280, rotation: 'portrait' }

/** A bridge answering each "METHOD path" from answers, 200 with no body otherwise. */
function bridge(answers: Record<string, Answer | Answer[]> = {}): { call: Call; requests: Request[] } {
  const requests: Request[] = []
  const call: Call = async (method, path, body) => {
    const request = { method, path, ...(body === undefined ? {} : { body }) }
    requests.push(request)
    const listed = answers[`${method} ${path}`]
    const queued = Array.isArray(listed) ? (listed.length > 1 ? listed.shift() : listed[0]) : listed
    const answer = typeof queued === 'function' ? queued(request) : (queued ?? {})
    const status = answer.status ?? 200

    return { status, ok: status < 300, text: answer.text ?? '', ...answer }
  }

  return { call, requests }
}

const json = (value: unknown): Partial<Reply> => ({ text: JSON.stringify(value) })

test('a screenshot reaches the model as a JPEG image with its size', async () => {
  const { call } = bridge({ 'GET /screen': json(SCREEN) })

  const outcome = await runAction(call, { action: 'screenshot' })

  expect(outcome).toEqual({
    blocks: [
      { type: 'image', source: { type: 'base64', media_type: 'image/jpeg', data: SCREEN.jpeg } },
      { type: 'text', text: expect.stringContaining('576×1280 px, portrait') },
    ],
  })
})

test('a tap and a long press send the point and how long the finger stays', async () => {
  const { call, requests } = bridge()

  const tapped = await runAction(call, { action: 'tap', x: 100, y: 200 })
  const pressed = await runAction(call, { action: 'tap', x: 0, y: 0, duration: 1 })
  const held = await runAction(call, { action: 'tap', x: 0, y: 0, duration: 0.3 })

  expect(requests).toEqual([
    { method: 'POST', path: '/gesture', body: { x: 100, y: 200, duration: 0 } },
    { method: 'POST', path: '/gesture', body: { x: 0, y: 0, duration: 1 } },
    { method: 'POST', path: '/gesture', body: { x: 0, y: 0, duration: 0.3 } },
  ])
  expect(tapped).toEqual({ blocks: [{ type: 'text', text: 'Tapped (100, 200).' }] })
  expect(pressed).toEqual({ blocks: [{ type: 'text', text: 'Long-pressed (0, 0).' }] })
  expect(held).toEqual({ blocks: [{ type: 'text', text: 'Tapped (0, 0).' }] })
})

test('a swipe sends both points', async () => {
  const { call, requests } = bridge()

  await runAction(call, { action: 'swipe', x: 10, y: 900, x2: 10, y2: 100, duration: 0.5 })

  expect(requests).toEqual([{ method: 'POST', path: '/gesture', body: { x: 10, y: 900, x2: 10, y2: 100, duration: 0.5 } }])
})

test('a gesture without its points is refused before the bridge is asked', async () => {
  const { call, requests } = bridge()

  const outcomes = [
    await runAction(call, { action: 'tap', x: 1 }),
    await runAction(call, { action: 'tap', x: '1', y: 2 }),
    await runAction(call, { action: 'swipe', x: 1, y: 2, x2: 3 }),
    await runAction(call, { action: 'tap', x: Number.NaN, y: 2 }),
  ]

  expect(outcomes.map(outcome => 'error' in outcome && outcome.error)).toEqual([
    'tap needs x, y as numbers.',
    'tap needs x, y as numbers.',
    'swipe needs x, y, x2, y2 as numbers.',
    'tap needs x, y as numbers.',
  ])
  expect(requests).toEqual([])
})

test('text and buttons go through the input route as keys the emulator knows', async () => {
  const { call, requests } = bridge()

  await runAction(call, { action: 'text', text: 'hello' })
  await runAction(call, { action: 'button', button: 'home' })
  await runAction(call, { action: 'button', button: 'delete' })
  const unknown = await runAction(call, { action: 'button', button: 'menu' })

  expect(requests).toEqual([
    { method: 'POST', path: '/input', body: { type: 'text', text: 'hello' } },
    { method: 'POST', path: '/input', body: { type: 'key', key: 'GoHome' } },
    { method: 'POST', path: '/input', body: { type: 'key', key: 'Backspace' } },
  ])
  expect(unknown).toEqual({ error: expect.stringMatching(/^button is one of home, back, /) })
})

test('open_url, launch, logs and rotate reach their routes', async () => {
  const { call, requests } = bridge({
    'POST /launch': json({ package: 'com.example' }),
    'POST /logs': json({ log: 'I/Example: up\n' }),
  })

  await runAction(call, { action: 'open_url', url: 'example://a?b=1&c=2' })
  const launched = await runAction(call, { action: 'launch', apk: '/builds/app.apk' })
  const logs = await runAction(call, { action: 'logs', package: 'com.example', lines: 50 })
  await runAction(call, { action: 'rotate', direction: 'left' })

  expect(requests).toEqual([
    { method: 'POST', path: '/open-url', body: { url: 'example://a?b=1&c=2' } },
    { method: 'POST', path: '/launch', body: { package: '', apk: '/builds/app.apk' } },
    { method: 'POST', path: '/logs', body: { package: 'com.example', lines: 50 } },
    { method: 'POST', path: '/rotate', body: { direction: 'left' } },
  ])
  expect(launched).toEqual({ blocks: [{ type: 'text', text: 'Installed and launched com.example.' }] })
  expect(logs).toEqual({ blocks: [{ type: 'text', text: 'I/Example: up\n' }] })
})

test('settings are read, changed or reset by what the call names', async () => {
  const { call, requests } = bridge({
    'GET /settings': json({ darkTheme: false }),
    'POST /settings': json({ darkTheme: true }),
    'POST /settings/reset': json({ darkTheme: false }),
  })

  const read = await runAction(call, { action: 'settings' })
  const changed = await runAction(call, { action: 'settings', dark_theme: true, density: 0 })
  await runAction(call, { action: 'settings', reset: true })

  expect(requests).toEqual([
    { method: 'GET', path: '/settings' },
    { method: 'POST', path: '/settings', body: { darkTheme: true, density: 0 } },
    { method: 'POST', path: '/settings/reset', body: {} },
  ])
  expect(read).toEqual({ blocks: [{ type: 'text', text: 'Device settings: {"darkTheme":false}' }] })
  expect(changed).toEqual({ blocks: [{ type: 'text', text: 'Device settings: {"darkTheme":true}' }] })
})

test("the bridge's refusal reaches the model, and no emulator points at start", async () => {
  const { call } = bridge({
    'POST /open-url': { status: 400, text: 'bad input: not a URL\n' },
    'GET /screen': { status: 503, text: 'no emulator is running\n' },
    'POST /rotate': { status: 500 },
  })

  expect(await runAction(call, { action: 'open_url', url: 'x' })).toEqual({ error: 'bad input: not a URL' })
  expect(await runAction(call, { action: 'screenshot' })).toEqual({
    error: 'no emulator is running: run this tool with action start first.',
  })
  expect(await runAction(call, { action: 'rotate', direction: 'right' })).toEqual({ error: 'the bridge answered 500' })
  expect(await runAction(call, { action: 'fly' })).toEqual({ error: expect.stringMatching(/^action is one of start, /) })
})

test('a bridge that cannot be reached is named', async () => {
  const call: Call = () => Promise.reject(new Error('connection refused'))

  expect(await runAction(call, { action: 'screenshot' })).toEqual({
    error: 'Cannot reach the Android bridge: connection refused',
  })
})

test('start boots the only virtual device and waits until it has booted', async () => {
  const starting = { connected: false, booted: false, avd: '', launch: { avd: 'Pixel_9', phase: 'starting' } }
  const { call, requests } = bridge({
    'GET /status': json(STOPPED),
    'GET /avds': json({ avds: [{ name: 'Pixel_9', running: false }] }),
    'GET /status?wait=booted': [json(starting), json({ connected: true, booted: false, avd: 'Pixel_9' }), json(BOOTED)],
  })

  const outcome = await runAction(call, { action: 'start' })

  expect(requests.map(request => `${request.method} ${request.path}`)).toEqual([
    'GET /status',
    'GET /avds',
    'POST /emulators/start',
    'GET /status?wait=booted',
    'GET /status?wait=booted',
    'GET /status?wait=booted',
  ])
  expect(requests[2]?.body).toEqual({ avd: 'Pixel_9' })
  expect(outcome).toEqual({ blocks: [{ type: 'text', text: 'Pixel_9 has booted.' }] })
})

test('start boots the named virtual device and refuses one that does not exist', async () => {
  const avds = json({ avds: [{ name: 'Pixel_9', running: false }, { name: 'Tablet', running: false }] })
  const { call, requests } = bridge({ 'GET /status': json(STOPPED), 'GET /avds': avds, 'GET /status?wait=booted': json(BOOTED) })

  await runAction(call, { action: 'start', avd: 'Tablet' })
  const missing = await runAction(call, { action: 'start', avd: 'Watch' })

  expect(requests.filter(request => request.path === '/emulators/start').map(request => request.body)).toEqual([{ avd: 'Tablet' }])
  expect(missing).toEqual({ error: 'No virtual device is named Watch; there are Pixel_9, Tablet.' })
})

test('start leaves a running emulator alone and joins a boot already under way', async () => {
  const running = bridge({ 'GET /status': json(BOOTED) })
  const booting = bridge({
    'GET /status': json({ connected: false, booted: false, avd: '', launch: { avd: 'Pixel_9', phase: 'starting' } }),
    'GET /status?wait=booted': json(BOOTED),
  })

  expect(await runAction(running.call, { action: 'start' })).toEqual({ blocks: [{ type: 'text', text: 'Pixel_9 is already running.' }] })
  await runAction(booting.call, { action: 'start' })

  expect(running.requests.length).toBe(1)
  expect(booting.requests.map(request => request.path)).toEqual(['/status', '/status?wait=booted'])
})

test('start waits for an emulator the bridge attached to as the start was asked', async () => {
  const avds = json({ avds: [{ name: 'Pixel_9', running: true }] })
  const busy = { status: 409, text: 'busy: an emulator is already running\n' }
  const attached = bridge({
    'GET /status': [json(STOPPED), json({ connected: true, booted: false, avd: 'Pixel_9' })],
    'GET /avds': avds,
    'POST /emulators/start': busy,
    'GET /status?wait=booted': json(BOOTED),
  })
  const elsewhere = bridge({ 'GET /status': json(STOPPED), 'GET /avds': avds, 'POST /emulators/start': { status: 409, text: 'busy: Pixel_9 is already running' } })

  expect(await runAction(attached.call, { action: 'start' })).toEqual({ blocks: [{ type: 'text', text: 'Pixel_9 has booted.' }] })
  expect(await runAction(elsewhere.call, { action: 'start' })).toEqual({ error: 'busy: Pixel_9 is already running' })
  expect(elsewhere.requests.filter(request => request.path === '/emulators/start').length).toBe(1)
})

test('start reports a launch that failed, no virtual device and a boot that outlasts it', async () => {
  const failed = bridge({
    'GET /status': json(STOPPED),
    'GET /avds': json({ avds: [{ name: 'Pixel_9', running: false }] }),
    'GET /status?wait=booted': json({ ...STOPPED, launch: { avd: 'Pixel_9', phase: 'failed', error: 'emulator: PANIC: Missing system image' } }),
  })
  const vanished = bridge({
    'GET /status': json({ connected: true, booted: false, avd: 'Pixel_9' }),
    'GET /status?wait=booted': json(STOPPED),
  })
  const none = bridge({ 'GET /status': json(STOPPED), 'GET /avds': json({ avds: [] }) })
  const unbooted = json({ connected: true, booted: false, avd: 'Pixel_9' })
  const slow = bridge({ 'GET /status': unbooted, 'GET /status?wait=booted': unbooted })

  expect(await runAction(failed.call, { action: 'start' })).toEqual({ error: 'emulator: PANIC: Missing system image' })
  expect(await runAction(vanished.call, { action: 'start' })).toEqual({ error: 'The emulator stopped before it booted.' })
  expect(await runAction(none.call, { action: 'start' })).toEqual({ error: expect.stringMatching(/^There is no virtual device/) })
  expect(await runAction(slow.call, { action: 'start' })).toEqual({
    error: 'The emulator is still booting; run start again to keep waiting.',
  })
  expect(slow.requests.filter(request => request.path === '/status?wait=booted').length).toBeGreaterThan(1)
})

function fetching(on: On, answer: (url: string, method: string, body?: string) => Partial<Reply>): Request[] {
  const seen: Request[] = []
  on('http.fetch', ($, e) => {
    seen.push({ method: e.init?.method ?? 'GET', path: e.url, body: e.init?.body })
    const reply = answer(e.url, e.init?.method ?? 'GET', e.init?.body)
    const status = reply.status ?? 200
    return { value: { status, ok: status < 300, headers: {}, text: reply.text ?? '' } }
  })

  return seen
}

const callTool = ($: Engine, input: Record<string, unknown>) => $.tool.call({ tool: TOOL, action: 'screenshot', ...input })

test('the tool starts the bridge and answers from it', async ($, on) => {
  const host = fakeHost(on, { ...MAC, exists: path => path.endsWith('/bin/android-bridge-darwin-arm64') })
  const seen = fetching(on, url => (url.endsWith('/screen') ? json(SCREEN) : {}))

  const shot = await callTool($, { action: 'screenshot' })
  await callTool($, { action: 'tap', x: 5, y: 6 })

  expect(host.spawned.length).toBe(1)
  expect(seen).toEqual([
    { method: 'GET', path: `${URL}/screen`, body: undefined },
    { method: 'POST', path: `${URL}/gesture`, body: '{"x":5,"y":6,"duration":0}' },
  ])
  expect(shot.result).toEqual([
    { type: 'image', source: { type: 'base64', media_type: 'image/jpeg', data: SCREEN.jpeg } },
    { type: 'text', text: expect.stringContaining('576×1280') },
  ])
  host.stopBridges()
})

test("the tool's refusals reach the model as errors", async ($, on) => {
  const missing = fakeHost(on, MAC)
  const unbuilt = await callTool($, { action: 'screenshot' })

  expect(unbuilt.deny).toMatch(/The Android bridge for this platform is missing/)
  expect(missing.spawned).toEqual([])
})

test('a refusal from the bridge reaches the model as an error', async ($, on) => {
  const host = fakeHost(on, { ...MAC, exists: path => path.endsWith('/bin/android-bridge-darwin-arm64') })
  fetching(on, () => ({ status: 503, text: 'no emulator is running' }))

  const answer = await callTool($, { action: 'screenshot' })

  expect(answer.deny).toBe('no emulator is running: run this tool with action start first.')
  host.stopBridges()
})
