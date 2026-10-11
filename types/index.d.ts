export type Device = {
  serial: string
  state: string
  model?: string
  isEmulator: boolean
}

export type Frame = {
  serial: string
  png: string
  jpeg: string
  width: number
  height: number
  generation: number
}

declare module 'claude-code' {
  interface PluginState {
    'android-emulator': {
      devices: Device[]
      selected: string | null
      frame: Frame | null
      problem: string | null
      isBusy: boolean
    }
  }
}
