export type Platform = {
  goos: 'darwin' | 'linux' | 'windows'
  goarch: 'arm64' | 'amd64'
}

const UNAME_SYSTEM: Record<string, Platform['goos']> = { Darwin: 'darwin', Linux: 'linux' }
const UNAME_MACHINE: Record<string, Platform['goarch']> = { arm64: 'arm64', aarch64: 'arm64', x86_64: 'amd64' }
const WINDOWS_ARCHITECTURE: Record<string, Platform['goarch']> = { ARM64: 'arm64', AMD64: 'amd64' }
const LIVE_VIEW = /^http:\/\/127\.0\.0\.1:[0-9]{1,5}$/

/** The platform `uname -sm` names, such as `Darwin arm64`; null for one no bridge is built for. */
export function unixPlatform(uname: string): Platform | null {
  const [system = '', machine = ''] = uname.trim().split(/\s+/)
  const goos = UNAME_SYSTEM[system]
  const goarch = UNAME_MACHINE[machine]

  return goos && goarch ? { goos, goarch } : null
}

/** The Windows platform `PROCESSOR_ARCHITECTURE` names; null for one no bridge is built for. */
export function windowsPlatform(architecture: string | undefined): Platform | null {
  const goarch = WINDOWS_ARCHITECTURE[architecture ?? '']

  return goarch ? { goos: 'windows', goarch } : null
}

export function executable(name: string, { goos }: Platform): string {
  return goos === 'windows' ? `${name}.exe` : name
}

/** Where a release ships the bridge built for platform. */
export function releaseBinary(root: string, platform: Platform): string {
  return executable(`${root}/bin/android-bridge-${platform.goos}-${platform.goarch}`, platform)
}

/** The live view's URL from the line the bridge writes to stdout once it listens; null for any other line. */
export function liveViewFrom(line: string): string | null {
  try {
    const announced: unknown = JSON.parse(line)
    const url = typeof announced === 'object' && announced !== null ? Reflect.get(announced, 'liveView') : null

    return typeof url === 'string' && LIVE_VIEW.test(url) ? url : null
  } catch {
    return null
  }
}

export const DEFAULT_IDLE_MINUTES = 15

/** The bridge's arguments: a free port, and the idle shutdown in whole seconds, the default for a value that is no count of minutes. */
export function bridgeArgs(idleMinutes: unknown): string[] {
  const minutes = typeof idleMinutes === 'number' && Number.isFinite(idleMinutes) && idleMinutes >= 0 ? idleMinutes : DEFAULT_IDLE_MINUTES

  return ['-port', '0', '-idle-shutdown', `${Math.round(minutes * 60)}s`]
}

export function openPrompt(url: string): string {
  return (
    `Open the Android emulator's live view, ${url}, in your built-in browser pane, not in Chrome. ` +
    'Only open it: the user starts and drives the emulator on that page.'
  )
}

export function lastLines(text: string, count: number): string {
  return text.trimEnd().split('\n').slice(-count).join('\n')
}
