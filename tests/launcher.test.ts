import { expect, mock, test } from 'claude-code/testing'
import type { Engine } from 'claude-code/testing'

import { announcing, fakeHost, MAC, URL } from './host'

function emulator($: Engine) {
  return $.command.run({
    command: 'emulator',
    args: '',
    origin: { kind: 'composer' },
    presentation: { isFullscreen: false, columns: 100 },
  })
}

const binaryFor = (name: string) => (path: string) => path.endsWith(`/bin/${name}`)

const PLATFORMS = [
  { host: { uname: 'Darwin arm64\n' }, binary: 'android-bridge-darwin-arm64' },
  { host: { uname: 'Linux x86_64\n' }, binary: 'android-bridge-linux-amd64' },
  { host: { uname: 'Linux aarch64\n' }, binary: 'android-bridge-linux-arm64' },
  { host: { windows: 'AMD64' }, binary: 'android-bridge-windows-amd64.exe' },
]

for (const { host, binary } of PLATFORMS) {
  test(`runs the bridge built for the host: ${binary}`, async ($, on) => {
    const seen = fakeHost(on, { ...host, exists: binaryFor(binary) })

    await emulator($)

    const checked = seen.checked.find(binaryFor(binary))
    expect(checked).toBeDefined()
    expect(seen.spawned).toEqual([[checked, '-port', '0', '-idle-shutdown', '900s']])
    seen.stopBridges()
  })
}

test('builds the bridge in a checkout', async ($, on) => {
  const seen = fakeHost(on, { ...MAC, exists: path => path.endsWith('/bridge/go.mod') })

  await emulator($)

  const built = '/tmp/t/claude-android-emulator/android-bridge-dev'
  const build = seen.ran.find(run => run.argv[1] === 'build')
  expect(build?.argv).toEqual(['go', 'build', '-o', built, './cmd/android-bridge'])
  expect(build?.cwd?.endsWith('/bridge')).toBe(true)
  expect(seen.spawned).toEqual([[built, '-port', '0', '-idle-shutdown', '900s']])
  seen.stopBridges()
})

test('passes the configured idle shutdown to the bridge', { options: { idleShutdownMinutes: 1.5 } }, async ($, on) => {
  const seen = fakeHost(on, { ...MAC, exists: binaryFor('android-bridge-darwin-arm64') })

  await emulator($)

  expect(seen.spawned[0]?.slice(1)).toEqual(['-port', '0', '-idle-shutdown', '90s'])
  seen.stopBridges()
})

test('an idle shutdown of 0 reaches the bridge as off', { options: { idleShutdownMinutes: 0 } }, async ($, on) => {
  const seen = fakeHost(on, { ...MAC, exists: binaryFor('android-bridge-darwin-arm64') })

  await emulator($)

  expect(seen.spawned[0]?.slice(1)).toEqual(['-port', '0', '-idle-shutdown', '0s'])
  seen.stopBridges()
})

test('reports a failed build', async ($, on) => {
  const seen = fakeHost(on, {
    ...MAC,
    exists: path => path.endsWith('/bridge/go.mod'),
    build: { exitCode: 1, stderr: './main.go:3:1: syntax error\n' },
  })

  const answer = await emulator($)

  expect(answer.text).toMatch(/Building the Android bridge failed/)
  expect(answer.text).toMatch(/syntax error/)
  expect(seen.spawned).toEqual([])
  expect(seen.prompts).toEqual([])
})

test('says which binary is missing', async ($, on) => {
  const seen = fakeHost(on, MAC)

  const answer = await emulator($)

  expect(answer.text).toMatch(/\/bin\/android-bridge-darwin-arm64$/)
  expect(seen.ran.some(run => run.argv[1] === 'build')).toBe(false)
  expect(seen.spawned).toEqual([])
  expect(seen.prompts).toEqual([])
})

test('asks Claude to open the live view', async ($, on) => {
  const clock = mock.clock(on)
  const seen = fakeHost(on, { ...MAC, exists: binaryFor('android-bridge-darwin-arm64') })

  const answer = await emulator($)
  await clock.settle()

  expect(answer.text).toContain(URL)
  expect(seen.prompts.length).toBe(1)
  expect(seen.prompts[0]).toContain(URL)
  expect(seen.prompts[0]).toMatch(/built-in browser pane/)
  seen.stopBridges()
})

test('reuses the running bridge', async ($, on) => {
  const clock = mock.clock(on)
  const seen = fakeHost(on, { ...MAC, exists: binaryFor('android-bridge-darwin-arm64') })

  await emulator($)
  const again = await emulator($)
  await clock.settle()

  expect(again.text).toContain(URL)
  expect(seen.spawned.length).toBe(1)
  expect(seen.prompts.length).toBe(2)
  expect(seen.prompts[1]).toBe(seen.prompts[0])
  seen.stopBridges()
})

test('reports a bridge that dies at start', async ($, on) => {
  let isDying = true
  const seen = fakeHost(on, {
    ...MAC,
    exists: binaryFor('android-bridge-darwin-arm64'),
    bridge: stopped => {
      if (!isDying) return announcing(stopped)
      isDying = false
      return (async function* () {
        yield { stream: 'stderr' as const, text: 'level=ERROR msg="cannot open port"\n' }
        return { code: 1, signal: null }
      })()
    },
  })

  const answer = await emulator($)
  expect(answer.text).toMatch(/exited before it announced its live view \(exit 1\)/)
  expect(answer.text).toMatch(/cannot open port/)
  expect(seen.prompts).toEqual([])

  const retried = await emulator($)
  expect(retried.text).toContain(URL)
  expect(seen.spawned.length).toBe(2)
  seen.stopBridges()
})

test('reports a bridge that stays silent', async ($, on) => {
  const clock = mock.clock(on)
  let wasStopped = false
  let isSilent = true
  const seen = fakeHost(on, {
    ...MAC,
    exists: binaryFor('android-bridge-darwin-arm64'),
    bridge: (stopped, killed) => {
      if (!isSilent) return announcing(stopped)
      isSilent = false
      return (async function* () {
        yield { stream: 'stderr' as const, text: 'level=INFO msg="waiting"\n' }
        await new Promise<void>(resolve => killed.addEventListener('abort', () => resolve()))
        wasStopped = true
        return { code: null, signal: 'SIGTERM' }
      })()
    },
  })

  const pending = emulator($)
  await clock.advance(8_000)
  const answer = await pending
  expect(answer.text).toMatch(/did not announce its live view within 8 s/)
  expect(answer.text).toMatch(/waiting/)
  await clock.settle()
  expect(wasStopped).toBe(true)

  const retried = await emulator($)
  expect(retried.text).toContain(URL)
  expect(seen.spawned.length).toBe(2)
  seen.stopBridges()
})

test('reports a bridge that stops later', async ($, on) => {
  const clock = mock.clock(on)
  let isFirst = true
  const seen = fakeHost(on, {
    ...MAC,
    exists: binaryFor('android-bridge-darwin-arm64'),
    bridge: stopped => {
      if (!isFirst) return announcing(stopped)
      isFirst = false
      return (async function* () {
        yield { stream: 'stdout' as const, text: `${JSON.stringify({ liveView: URL })}\n` }
        await stopped
        return { code: 2, signal: null }
      })()
    },
  })

  await emulator($)
  seen.stopBridges()
  await clock.settle()

  expect(seen.toasts).toEqual(['The Android bridge stopped (exit 2). Run /emulator to start it again.'])
  await emulator($)
  expect(seen.spawned.length).toBe(2)
  seen.stopBridges()
})

test('falls back to a Go in a common place', async ($, on) => {
  const seen = fakeHost(on, {
    ...MAC,
    exists: path => path.endsWith('/bridge/go.mod') || path === '/opt/homebrew/bin/go',
    refuse: argv => (argv[0] === 'go' ? 'go: not found' : undefined),
  })

  await emulator($)

  expect(seen.ran.find(run => run.argv[1] === 'build')?.argv[0]).toBe('/opt/homebrew/bin/go')
  seen.stopBridges()
})

test('reports a build that could not run, once', async ($, on) => {
  const seen = fakeHost(on, {
    ...MAC,
    exists: path => path.endsWith('/bridge/go.mod'),
    refuse: argv => (argv[1] === 'build' ? 'timed out after 300000 ms' : undefined),
  })

  const answer = await emulator($)

  expect(answer.text).toMatch(/Building the Android bridge failed:\n.*timed out/)
  expect(seen.ran.filter(run => run.argv[1] === 'build').length).toBe(1)
  expect(seen.spawned).toEqual([])
})

test('says so when the platform cannot be told', async ($, on) => {
  const seen = fakeHost(on, { refuse: argv => (argv[0] === 'uname' ? 'uname: not found' : undefined) })

  const answer = await emulator($)

  expect(answer.text).toMatch(/no bridge for this platform/)
  expect(seen.spawned).toEqual([])
})

test('a stopped bridge that exits late leaves the next one in place', async ($, on) => {
  const clock = mock.clock(on)
  let isFirst = true
  const seen = fakeHost(on, {
    ...MAC,
    exists: binaryFor('android-bridge-darwin-arm64'),
    bridge: stopped => {
      if (!isFirst) return announcing(stopped)
      isFirst = false
      return (async function* () {
        await stopped
        return { code: null, signal: 'SIGTERM' }
      })()
    },
  })

  const pending = emulator($)
  await clock.advance(8_000)
  await pending
  await emulator($)
  seen.stopBridge(0)
  await clock.settle()
  const again = await emulator($)

  expect(again.text).toContain(URL)
  expect(seen.spawned.length).toBe(2)
  seen.stopBridges()
})

test('names the live view in a toast when the prompt is dropped', async ($, on) => {
  const clock = mock.clock(on)
  const seen = fakeHost(on, { ...MAC, exists: binaryFor('android-bridge-darwin-arm64'), dropPrompt: true })

  await emulator($)
  await clock.settle()

  expect(seen.toasts).toEqual([`Open ${URL} in the browser pane to see the emulator.`])
  seen.stopBridges()
})
