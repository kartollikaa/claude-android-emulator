import { expect, mock, test } from 'claude-code/testing'
import type { Engine } from 'claude-code/testing'
import type { On, ProcessSpawnChunk, ProcessSpawnResult } from 'claude-code'

const URL = 'http://127.0.0.1:52833'
const MAC = { uname: 'Darwin arm64\n' }

type Bridge = (stopped: Promise<void>, killed: AbortSignal) => AsyncGenerator<ProcessSpawnChunk, ProcessSpawnResult>

type Host = {
  uname?: string
  windows?: string
  exists?: (path: string) => boolean
  build?: { exitCode: number; stderr: string }
  refuse?: (argv: readonly string[]) => string | undefined
  dropPrompt?: boolean
  bridge?: Bridge
}

type Seen = {
  ran: { argv: string[]; cwd?: string }[]
  checked: string[]
  spawned: string[][]
  prompts: string[]
  toasts: string[]
  stopBridge: (index: number) => void
  stopBridges: () => void
}

async function* announcing(stopped: Promise<void>): AsyncGenerator<ProcessSpawnChunk, ProcessSpawnResult> {
  yield { stream: 'stderr', text: 'level=INFO msg="live view ready"\n' }
  yield { stream: 'stdout', text: `${JSON.stringify({ liveView: URL })}\n` }
  await stopped
  return { code: 0, signal: null }
}

function fakeHost(on: On, host: Host): Seen {
  const stops: (() => void)[] = []
  const seen: Seen = {
    ran: [],
    checked: [],
    spawned: [],
    prompts: [],
    toasts: [],
    stopBridge: index => stops[index]?.(),
    stopBridges: () => stops.forEach(stop => stop()),
  }
  on('env.get', ($, e) => {
    const values: Record<string, string | undefined> = {
      OS: host.windows === undefined ? undefined : 'Windows_NT',
      PROCESSOR_ARCHITECTURE: host.windows,
      TMPDIR: '/tmp/t/',
    }
    return { value: values[e.name] }
  })
  on('fs.exists', ($, e) => {
    seen.checked.push(e.path)
    return { value: host.exists?.(e.path) ?? false }
  })
  on('process.run', ($, e) => {
    seen.ran.push({ argv: [...e.argv], cwd: e.init?.cwd })
    const refusal = host.refuse?.(e.argv)
    if (refusal !== undefined) return { deny: refusal }
    const isUname = e.argv[0] === 'uname'
    const result = isUname
      ? { exitCode: host.uname === undefined ? 127 : 0, stdout: host.uname ?? '', stderr: '' }
      : { exitCode: 0, stdout: '', stderr: '', ...(e.argv[1] === 'build' ? host.build : {}) }
    return { value: { ...result, isStdoutTruncated: false, isStderrTruncated: false } }
  })
  on('process.spawn', async function* ($, e, next) {
    seen.spawned.push([...e.argv])
    const stopped = new Promise<void>(resolve => stops.push(resolve))
    const ended = yield* (host.bridge ?? announcing)(stopped, next.signal)
    return { value: ended }
  })
  on('prompt.submit', ($, e) => {
    seen.prompts.push(e.text)
    return host.dropPrompt ? { drop: 'the session is not taking prompts' } : { text: e.text }
  })
  on('ui.toast', ($, e) => {
    seen.toasts.push(e.text)
    return { value: undefined }
  })
  return seen
}

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
    expect(seen.spawned).toEqual([[checked, '-port', '0']])
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
  expect(seen.spawned).toEqual([[built, '-port', '0']])
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
