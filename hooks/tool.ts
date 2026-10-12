import type { ToolSpec } from 'claude-code'

export const TOOL = 'mcp__android-emulator__emulator'

declare module 'claude-code' {
  interface McpToolInputs {
    'mcp__android-emulator__emulator': { action: string; [argument: string]: unknown }
  }
}

// start gives up after this many of the bridge's boot long-polls.
const BOOT_WAITS = 12

const BUTTON_KEYS: Record<string, string> = {
  home: 'GoHome',
  back: 'GoBack',
  recents: 'AppSwitch',
  power: 'Power',
  enter: 'Enter',
  delete: 'Backspace',
  volume_up: 'AudioVolumeUp',
  volume_down: 'AudioVolumeDown',
}

const ACTIONS = ['start', 'screenshot', 'tap', 'swipe', 'text', 'button', 'open_url', 'launch', 'logs', 'settings', 'rotate'] as const

export const toolSpec: ToolSpec = {
  name: 'emulator',
  description: [
    'See and drive the Android device the /emulator live view shows: an emulator, or a phone connected over USB or Wi-Fi.',
    'start boots a virtual device when none runs and waits for it (with a phone shown it only says so); screenshot returns the screen as an image.',
    "tap, swipe and their x/y take pixels of the latest screenshot, origin top-left; take one first, and again after the screen turns.",
    "text types into the focused field (a phone types only what its key map has, ASCII), button presses a hardware or navigation key, open_url opens a link or deep link,",
    'launch starts an installed app by package or installs an .apk first, logs reads logcat (one app\'s when package is given),',
    'settings reads or changes the dark theme, font scale, display density and language (reset restores the defaults),',
    "rotate turns the device a quarter (a phone's locked rotation, auto-rotate off). Look at a screenshot after an action to see its effect.",
  ].join(' '),
  inputSchema: {
    type: 'object',
    properties: {
      action: { type: 'string', enum: ACTIONS },
      avd: { type: 'string', description: 'start: the virtual device to boot; left out, the only one, else the first listed' },
      x: { type: 'number', description: 'tap, swipe: where the finger goes down, in screenshot pixels' },
      y: { type: 'number' },
      x2: { type: 'number', description: 'swipe: where the finger lifts' },
      y2: { type: 'number' },
      duration: { type: 'number', description: 'tap, swipe: seconds the finger stays down; a tap held longer than 0.5 is a long press' },
      text: { type: 'string', description: 'text: what to type' },
      button: { type: 'string', enum: Object.keys(BUTTON_KEYS) },
      url: { type: 'string', description: 'open_url: a URL or deep link with its scheme' },
      package: { type: 'string', description: 'launch, logs: the application id' },
      apk: { type: 'string', description: 'launch: the absolute path of an .apk to install first' },
      lines: { type: 'integer', description: 'logs: how many of the newest lines' },
      dark_theme: { type: 'boolean' },
      font_scale: { type: 'number', description: 'settings: 1 is the default' },
      density: { type: 'integer', description: 'settings: display density in dpi; 0 returns to the physical density' },
      locale: { type: 'string', description: 'settings: a BCP 47 language tag such as en-US' },
      reset: { type: 'boolean', description: 'settings: light theme, default font and display size first, then any change named beside it; the language stays' },
      direction: { type: 'string', enum: ['left', 'right'], description: 'rotate: counter-clockwise (left) or clockwise' },
    },
    required: ['action'],
  },
  isDeferred: false,
}

export type Reply = { status: number; ok: boolean; text: string }
export type Call = (method: 'GET' | 'POST', path: string, body?: unknown) => Promise<Reply>
export type Block =
  | { type: 'text'; text: string }
  | { type: 'image'; source: { type: 'base64'; media_type: 'image/jpeg'; data: string } }
export type Outcome = { blocks: Block[] } | { error: string }

type Input = Record<string, unknown>
type Screen = { jpeg: string; width: number; height: number; rotation: string }
type State = {
  connected: boolean
  booted: boolean
  avd: string
  kind?: string
  model?: string
  serial?: string
  phoneError?: string
  launch?: { avd: string; phase: string; error?: string }
}

const said = (text: string): Outcome => ({ blocks: [{ type: 'text', text }] })

class Refusal extends Error {}

function refuse(text: string): never {
  throw new Refusal(text)
}

function numbers(input: Input, ...names: string[]): Record<string, number> {
  const found: Record<string, number> = {}
  for (const name of names) {
    const value = input[name]
    if (typeof value !== 'number' || !Number.isFinite(value)) refuse(`${input.action} needs ${names.join(', ')} as numbers.`)
    found[name] = value
  }

  return found
}

function text(input: Input, name: string): string {
  const value = input[name]
  if (typeof value !== 'string' || value === '') refuse(`${input.action} needs ${name}.`)

  return value
}

function refuseReply(reply: Reply): never {
  const reason = reply.text.trim() || `the bridge answered ${reply.status}`
  refuse(reply.status === 503 && reason.includes('no emulator') ? `${reason}: run this tool with action start first.` : reason)
}

async function answer<T>(call: Call, method: 'GET' | 'POST', path: string, body?: unknown): Promise<T> {
  const reply = await call(method, path, body)
  if (!reply.ok) refuseReply(reply)

  if (reply.text.trim() === '') return undefined as T
  try {
    return JSON.parse(reply.text) as T
  } catch {
    return refuse(`The bridge answered ${method} ${path} with something that is not JSON: ${reply.text.slice(0, 200)}`)
  }
}

// runAction carries out one call of the tool against the bridge.
export async function runAction(call: Call, input: Input): Promise<Outcome> {
  try {
    return await act(call, input)
  } catch (error) {
    if (error instanceof Refusal) return { error: error.message }
    return { error: `Cannot reach the Android bridge: ${error instanceof Error ? error.message : String(error)}` }
  }
}

async function act(call: Call, input: Input): Promise<Outcome> {
  switch (input.action) {
    case 'start':
      return start(call, typeof input.avd === 'string' ? input.avd : undefined)
    case 'screenshot': {
      const screen = await answer<Screen>(call, 'GET', '/screen')
      return {
        blocks: [
          { type: 'image', source: { type: 'base64', media_type: 'image/jpeg', data: screen.jpeg } },
          { type: 'text', text: `Screenshot ${screen.width}×${screen.height} px, ${screen.rotation}; tap and swipe take its pixels.` },
        ],
      }
    }
    case 'tap': {
      const { x, y } = numbers(input, 'x', 'y')
      await answer(call, 'POST', '/gesture', { x, y, duration: input.duration ?? 0 })
      return said(`${typeof input.duration === 'number' && input.duration > 0.5 ? 'Long-pressed' : 'Tapped'} (${x}, ${y}).`)
    }
    case 'swipe': {
      const { x, y, x2, y2 } = numbers(input, 'x', 'y', 'x2', 'y2')
      await answer(call, 'POST', '/gesture', { x, y, x2, y2, duration: input.duration ?? 0 })
      return said(`Swiped from (${x}, ${y}) to (${x2}, ${y2}).`)
    }
    case 'text':
      await answer(call, 'POST', '/input', { type: 'text', text: text(input, 'text') })
      return said('Typed the text.')
    case 'button': {
      const key = BUTTON_KEYS[text(input, 'button')] ?? refuse(`button is one of ${Object.keys(BUTTON_KEYS).join(', ')}.`)
      await answer(call, 'POST', '/input', { type: 'key', key })
      return said(`Pressed ${input.button}.`)
    }
    case 'open_url':
      await answer(call, 'POST', '/open-url', { url: text(input, 'url') })
      return said(`Opened ${input.url}.`)
    case 'launch': {
      const launched = await answer<{ package: string }>(call, 'POST', '/launch', { package: input.package ?? '', apk: input.apk ?? '' })
      return said(`${input.apk ? 'Installed and launched' : 'Launched'} ${launched.package}.`)
    }
    case 'logs': {
      const { log } = await answer<{ log: string }>(call, 'POST', '/logs', { package: input.package ?? '', lines: input.lines ?? 0 })
      return said(log.trim() === '' ? 'The log is empty.' : log)
    }
    case 'settings':
      return settings(call, input)
    case 'rotate':
      await answer(call, 'POST', '/rotate', { direction: text(input, 'direction') })
      return said(`Rotated ${input.direction}; take a screenshot for the new screen size.`)
  }

  return refuse(`action is one of ${ACTIONS.join(', ')}.`)
}

async function settings(call: Call, input: Input): Promise<Outcome> {
  const change = {
    darkTheme: input.dark_theme,
    fontScale: input.font_scale,
    density: input.density,
    locale: input.locale,
  }
  const isChange = Object.values(change).some(value => value !== undefined)
  let current = input.reset === true ? await answer(call, 'POST', '/settings/reset', {}) : undefined
  if (isChange) current = await answer(call, 'POST', '/settings', change)
  current ??= await answer(call, 'GET', '/settings')

  return said(`Device settings: ${JSON.stringify(current)}`)
}

const isUnderWay = (state: State) => state.connected || state.launch?.phase === 'starting'

async function start(call: Call, avd: string | undefined): Promise<Outcome> {
  let state = await answer<State>(call, 'GET', '/status')
  if (state.connected && state.kind === 'device') {
    return said(`A phone is connected, ${state.model || state.serial}, and the other actions drive it; start boots no emulator while it shows.`)
  }
  if (state.booted && avd !== undefined && state.avd !== '' && state.avd !== avd) {
    refuse(`${state.avd} is running, not ${avd}; shut it down from the live view first.`)
  }
  if (state.booted) return said(`${state.avd || 'An emulator'} is already running.`)
  if (!isUnderWay(state)) {
    const { avds } = await answer<{ avds: { name: string; running: boolean }[] }>(call, 'GET', '/avds')
    const noPhone = state.phoneError ? ` A phone is connected but cannot show: ${state.phoneError}.` : ''
    const chosen = avd ?? avds[0]?.name ?? refuse(`There is no virtual device; create one in Android Studio's Device Manager.${noPhone}`)
    if (!avds.some(listed => listed.name === chosen)) refuse(`No virtual device is named ${chosen}; there are ${avds.map(listed => listed.name).join(', ')}.`)
    const started = await call('POST', '/emulators/start', { avd: chosen })
    // A refusal can be an emulator the bridge attached to since the status above: wait for that one.
    if (!started.ok && !isUnderWay(await answer<State>(call, 'GET', '/status'))) refuseReply(started)
  }
  for (let poll = 0; poll < BOOT_WAITS; poll++) {
    state = await answer<State>(call, 'GET', '/status?wait=booted')
    if (state.booted) return said(`${state.avd} has booted.`)
    if (state.launch?.phase === 'failed') refuse(state.launch.error ?? `${state.launch.avd} did not start.`)
    if (!isUnderWay(state)) refuse('The emulator stopped before it booted.')
  }

  return refuse('The emulator is still booting; run start again to keep waiting.')
}
