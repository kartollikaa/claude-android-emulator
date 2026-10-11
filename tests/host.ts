import type { On, ProcessSpawnChunk, ProcessSpawnResult } from 'claude-code'

export const URL = 'http://127.0.0.1:52833'
export const MAC = { uname: 'Darwin arm64\n' }

export type Bridge = (stopped: Promise<void>, killed: AbortSignal) => AsyncGenerator<ProcessSpawnChunk, ProcessSpawnResult>

export type Host = {
  uname?: string
  windows?: string
  exists?: (path: string) => boolean
  build?: { exitCode: number; stderr: string }
  refuse?: (argv: readonly string[]) => string | undefined
  dropPrompt?: boolean
  bridge?: Bridge
}

export type Seen = {
  ran: { argv: string[]; cwd?: string }[]
  checked: string[]
  spawned: string[][]
  prompts: string[]
  toasts: string[]
  stopBridge: (index: number) => void
  stopBridges: () => void
}

export async function* announcing(stopped: Promise<void>): AsyncGenerator<ProcessSpawnChunk, ProcessSpawnResult> {
  yield { stream: 'stderr', text: 'level=INFO msg="live view ready"\n' }
  yield { stream: 'stdout', text: `${JSON.stringify({ liveView: URL })}\n` }
  await stopped
  return { code: 0, signal: null }
}

export function fakeHost(on: On, host: Host): Seen {
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
