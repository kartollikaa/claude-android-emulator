import type { EngineInterface, Register } from 'claude-code'

import {
  executable,
  lastLines,
  liveViewFrom,
  openPrompt,
  releaseBinary,
  unixPlatform,
  windowsPlatform,
} from './bridge'
import type { Platform } from './bridge'

// A hook's own time is capped at 10 s, and /emulator waits for the URL inside its hook.
const ANNOUNCE_MS = 8_000
const BUILD_TIMEOUT_MS = 300_000
const STDERR_KEPT = 4_000
const GO_CANDIDATES = ['go', '/opt/homebrew/bin/go', '/usr/local/go/bin/go', '/usr/local/bin/go']

type Started = { url: string } | { problem: string }
type Found = { binary: string } | { problem: string }

let liveView: string | null = null
let starting: Promise<Started> | null = null

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    const started = await next(e)
    await $.command.register({
      name: 'emulator',
      description: 'Open the Android emulator in the browser pane',
    })

    return started
  })

  on('command.run', { command: 'emulator' }, async $ => {
    const started = await ensureBridge($)
    if ('problem' in started) return { text: started.problem }
    $.clock.after(0, () => void $.prompt.submit({ text: openPrompt(started.url) }))

    return { text: `Android emulator live view: ${started.url}` }
  })
}

function ensureBridge($: EngineInterface): Promise<Started> {
  if (liveView !== null) return Promise.resolve({ url: liveView })
  starting ??= startBridge($).finally(() => {
    starting = null
  })

  return starting
}

async function startBridge($: EngineInterface): Promise<Started> {
  const found = await findBridge($)

  return 'problem' in found ? found : runBridge($, found.binary)
}

async function findBridge($: EngineInterface): Promise<Found> {
  const platform = await hostPlatform($)
  if (platform === null) return { problem: 'The Android emulator mod has no bridge for this platform.' }
  const binary = releaseBinary($.plugin.root, platform)
  if (await $.fs.exists(binary)) return { binary }
  if (await $.fs.exists(`${$.plugin.root}/bridge/go.mod`)) return buildBridge($, platform)

  return { problem: `The Android bridge for this platform is missing: ${binary}` }
}

async function hostPlatform($: EngineInterface): Promise<Platform | null> {
  if ((await $.env.get('OS')) === 'Windows_NT') {
    return windowsPlatform(await $.env.get('PROCESSOR_ARCHITECTURE'))
  }
  const uname = await $.process.run(['uname', '-sm'])

  return uname.exitCode === 0 ? unixPlatform(uname.stdout) : null
}

/** Builds the bridge from this checkout's sources: development only, a release ships the binaries. */
async function buildBridge($: EngineInterface, platform: Platform): Promise<Found> {
  const binary = executable(`${await scratchDirectory($)}/android-bridge-dev`, platform)
  for (const go of GO_CANDIDATES) {
    if (go.startsWith('/') && !(await $.fs.exists(go))) continue
    const built = await $.process
      .run([go, 'build', '-o', binary, './cmd/android-bridge'], {
        cwd: `${$.plugin.root}/bridge`,
        timeoutMs: BUILD_TIMEOUT_MS,
      })
      .catch(() => null)
    if (built === null) continue
    if (built.exitCode !== 0) {
      return { problem: `Building the Android bridge failed:\n${lastLines(built.stderr, 20)}` }
    }

    return { binary }
  }

  return { problem: 'The Android bridge is not built, and no Go toolchain was found to build it from bridge/.' }
}

async function scratchDirectory($: EngineInterface): Promise<string> {
  const temporary = (await $.env.get('TMPDIR')) ?? (await $.env.get('TEMP')) ?? '/tmp'

  return `${temporary.replace(/[\\/]$/, '')}/claude-android-emulator`
}

function runBridge($: EngineInterface, binary: string): Promise<Started> {
  return new Promise(resolve => {
    let stdout = ''
    let stderr = ''
    let isAnnounced = false
    const bridge = $.process.spawn({ argv: [binary, '-port', '0'] })
    const silence = $.clock.after(ANNOUNCE_MS, () => {
      resolve({
        problem: `The Android bridge did not announce its live view within ${ANNOUNCE_MS / 1000} s and was stopped.\n${lastLines(stderr, 10)}`,
      })
      void bridge.return({ code: null, signal: null })
    })
    void (async () => {
      try {
        for await (const { stream, text } of bridge) {
          if (stream === 'stderr') {
            stderr = (stderr + text).slice(-STDERR_KEPT)
            continue
          }
          stdout += text
          const url = isAnnounced || !stdout.includes('\n') ? null : liveViewFrom(stdout.split('\n')[0] ?? '')
          if (url !== null) {
            isAnnounced = true
            liveView = url
            silence.cancel()
            resolve({ url })
          }
        }
      } catch (error) {
        silence.cancel()
        resolve({ problem: `The Android bridge could not start: ${error instanceof Error ? error.message : String(error)}` })
        return
      }
      silence.cancel()
      liveView = null
      const ended = await bridge.result.catch(() => null)
      const how = ended?.signal ? ` (${ended.signal})` : ended?.code != null ? ` (exit ${ended.code})` : ''
      if (isAnnounced) {
        $.ui.toast(`The Android bridge stopped${how}. Run /emulator to start it again.`)
      } else {
        resolve({ problem: `The Android bridge exited before it announced its live view${how}:\n${lastLines(stderr, 10)}` })
      }
    })()
  })
}
