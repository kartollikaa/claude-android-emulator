import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register, Timer } from 'claude-code'

import {
  CAPTURE_SCRIPT,
  adbCandidates,
  fitCells,
  parseDevices,
  parseSipsSize,
  screenSvg,
} from './adb'
import type { LiveMode } from '../types'
import {
  DEFAULT_CELL_METRICS,
  FILE_POLL_MS,
  HELPER,
  POLL_MS,
  PROBE_VARIANTS,
  RATE_WINDOW_MS,
  displaySize,
  measuredMetrics,
  overlayCells,
  parseFileFrame,
  parseLiveFrame,
  streamMarkdown,
  streamSvg,
} from './live'

const PANE = 'android'
const PREVIEW_LONG_SIDE = 720
const PREVIEW_MAX_HEIGHT = 640
const PANE_CHROME_ROWS = 6
const NAV_KEYS = [
  { key: 'GoBack', label: 'Back' },
  { key: 'GoHome', label: 'Home' },
  { key: 'AppSwitch', label: 'Recents' },
] as const

const devices = atom({ plugin: 'android-emulator', key: 'devices' } as const, [])
const selected = atom({ plugin: 'android-emulator', key: 'selected' } as const, null)
const frame = atom({ plugin: 'android-emulator', key: 'frame' } as const, null)
const problem = atom({ plugin: 'android-emulator', key: 'problem' } as const, null)
const isBusy = atom({ plugin: 'android-emulator', key: 'isBusy' } as const, false)
const isLive = atom({ plugin: 'android-emulator', key: 'isLive' } as const, false)
const liveFrame = atom({ plugin: 'android-emulator', key: 'liveFrame' } as const, null)
const liveRate = atom({ plugin: 'android-emulator', key: 'liveRate' } as const, 0)
const liveMode = atom({ plugin: 'android-emulator', key: 'liveMode' } as const, 'svg')
const liveFile = atom({ plugin: 'android-emulator', key: 'liveFile' } as const, null)
const isProbing = atom({ plugin: 'android-emulator', key: 'isProbing' } as const, false)
const touchStatus = atom({ plugin: 'android-emulator', key: 'touchStatus' } as const, null)
const cellMetrics = atom({ plugin: 'android-emulator', key: 'cellMetrics' } as const, DEFAULT_CELL_METRICS)

let poller: Timer | null = null
let isFetching = false
let framesCounted = 0
let countingSince = 0
let lastFileSeq = -1

async function sendInput($: EngineInterface, message: Record<string, unknown>): Promise<void> {
  const response = await $.http.fetch(`${HELPER}/input`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(message),
  })
  if (!response.ok) {
    throw new Error(`live helper answered ${response.status}: ${response.text}`)
  }
}

async function pollSvg($: EngineInterface): Promise<void> {
  const after = (await read($, liveFrame))?.seq ?? -1
  const response = await $.http.fetch(`${HELPER}/frame?after=${after}`)
  if (!response.ok) {
    throw new Error(`live helper answered ${response.status}`)
  }
  const next = parseLiveFrame(response.text)
  if (next !== null) {
    await update($, liveFrame, () => next)
    framesCounted += 1
  }
}

async function pollFile($: EngineInterface, isRateTick: boolean): Promise<void> {
  const response = await $.http.fetch(`${HELPER}/frame-file?after=${lastFileSeq}`)
  if (!response.ok) {
    throw new Error(`live helper answered ${response.status}`)
  }
  const next = parseFileFrame(response.text)
  if (next === null) return
  lastFileSeq = next.seq
  framesCounted += 1
  const drawn = await read($, liveFile)
  const isNewSize = drawn === null || drawn.width !== next.width || drawn.height !== next.height
  if (isNewSize || isRateTick) {
    await update($, liveFile, () => next)
    return
  }
  const blitted = await $.ui.blit({
    requestId: PANE,
    key: 'screen',
    source: { file: next.path, format: 'rgb', width: next.width, height: next.height, generation: next.seq },
  })
  if ('deny' in blitted && blitted.deny !== undefined) {
    await update($, liveFile, () => next)
  }
}

async function poll($: EngineInterface): Promise<void> {
  if (isFetching) return
  isFetching = true
  try {
    const now = await $.clock.now()
    const isRateTick = now - countingSince >= RATE_WINDOW_MS
    if ((await read($, liveMode)) === 'file') {
      await pollFile($, isRateTick)
    } else {
      await pollSvg($)
    }
    if (isRateTick) {
      const rate = Math.round((framesCounted * 1000) / (now - countingSince))
      framesCounted = 0
      countingSince = now
      await update($, liveRate, () => rate)
    }
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error)
    await stopLive($)
    await update($, problem, () =>
      `Live helper not reachable at ${HELPER} (${reason}). Start it: node prototypes/live-helper/server.mjs`,
    )
  } finally {
    isFetching = false
  }
}

async function startLive($: EngineInterface, mode: LiveMode): Promise<void> {
  await update($, liveMode, () => mode)
  await update($, isLive, () => true)
  await update($, problem, () => null)
  if (poller === null) {
    framesCounted = 0
    lastFileSeq = -1
    countingSince = await $.clock.now()
    poller = $.clock.every(mode === 'file' ? FILE_POLL_MS : POLL_MS, () => void poll($))
  }
  await poll($)
}

async function stopLive($: EngineInterface): Promise<void> {
  poller?.cancel()
  poller = null
  await update($, isLive, () => false)
  await update($, liveRate, () => 0)
  await update($, liveFile, () => null)
}

async function resolveAdb($: EngineInterface): Promise<string> {
  const candidates = adbCandidates({
    androidHome: await $.env.get('ANDROID_HOME'),
    sdkRoot: await $.env.get('ANDROID_SDK_ROOT'),
    home: await $.env.get('HOME'),
  })

  for (const candidate of candidates) {
    if (await $.fs.exists(candidate)) {
      return candidate
    }
  }

  return 'adb'
}

async function frameDirectory($: EngineInterface): Promise<string> {
  const tmp = (await $.env.get('TMPDIR')) ?? '/tmp'

  return `${tmp.replace(/\/$/, '')}/claude-android-emulator`
}

async function refreshDevices($: EngineInterface): Promise<string> {
  const adb = await resolveAdb($)
  const listed = await $.process.run([adb, 'devices', '-l'], { timeoutMs: 10_000 })

  if (listed.exitCode !== 0) {
    throw new Error(listed.stderr.trim() || `adb devices exited with ${listed.exitCode}`)
  }

  const found = parseDevices(listed.stdout)
  await update($, devices, () => found)
  await update($, selected, current =>
    current !== null && found.some(device => device.serial === current)
      ? current
      : (found.find(device => device.state === 'device')?.serial ?? null),
  )

  return adb
}

async function capture($: EngineInterface, adb: string, serial: string): Promise<void> {
  const directory = await frameDirectory($)
  const png = `${directory}/screen.png`
  const jpeg = `${directory}/screen.jpg`

  const shot = await $.process.run(['sh', '-c', CAPTURE_SCRIPT, adb, serial, png], {
    timeoutMs: 15_000,
  })
  if (shot.exitCode !== 0) {
    throw new Error(shot.stderr.trim() || `screencap on ${serial} failed`)
  }

  const probed = await $.process.run(['sips', '-g', 'pixelWidth', '-g', 'pixelHeight', png])
  const size = parseSipsSize(probed.stdout)
  if (size === null) {
    throw new Error(`could not read the size of ${png}`)
  }

  await $.process.run([
    'sips', '-s', 'format', 'jpeg', '-s', 'formatOptions', '55',
    '-Z', String(PREVIEW_LONG_SIDE), png, '--out', jpeg,
  ])

  await update($, frame, previous => ({
    serial,
    png,
    jpeg,
    ...size,
    generation: (previous?.generation ?? 0) + 1,
  }))
}

async function refresh($: EngineInterface): Promise<void> {
  await update($, isBusy, () => true)
  try {
    const adb = await refreshDevices($)
    const serial = await read($, selected)
    if (serial !== null) {
      await capture($, adb, serial)
    }
    await update($, problem, () => null)
  } catch (error) {
    await update($, problem, () => (error instanceof Error ? error.message : String(error)))
  } finally {
    await update($, isBusy, () => false)
  }
}

async function select($: EngineInterface, serial: string): Promise<void> {
  await update($, selected, () => serial)
  await refresh($)
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'android',
      description: 'Show the Android emulator or device screen in a pane',
    })

    if (await read($, isLive)) {
      void startLive($, await read($, liveMode))
    }

    return next(e)
  })

  on('ui.close', async ($, e, next) => {
    if (e.id === PANE) {
      await stopLive($)
    }

    return next(e)
  }).catch(($, e, next) => next(e))

  on('ui.message', async ($, e) => {
    if (e.requestId !== PANE) return {}
    const data = (e.data ?? {}) as Record<string, unknown>
    try {
      if (data.type === 'ruler' && (e.element === 'ruler-x' || e.element === 'ruler-y')) {
        const shot = await read($, liveFrame)
        if (shot !== null) {
          const ruler = e.element
          const cells = { columns: Number(data.columns), rows: Number(data.rows) }
          await update($, cellMetrics, metrics =>
            measuredMetrics(metrics, ruler, displaySize(shot.width, shot.height), cells),
          )
        }
        await sendInput($, { ...data, type: 'debug', kind: e.element, surface: e.surface })
      } else if (data.type === 'overlay') {
        await update($, touchStatus, () => `Touch: ready, overlay ${data.columns}x${data.rows} cells`)
        await sendInput($, { ...data, type: 'debug', kind: data.type, surface: e.surface })
      } else if (data.type === 'pointer') {
        const hasSize = Number(data.columns) > 0 && Number(data.rows) > 0
        const at = `${Number(data.x).toFixed(2)}, ${Number(data.y).toFixed(2)}`
        await update($, touchStatus, () =>
          hasSize ? `Touch: down at ${at}` : 'Touch: the overlay has no size on this surface',
        )
        await sendInput($, { ...data, type: 'debug', kind: data.type, surface: e.surface })
      } else if (data.type === 'gesture' && Array.isArray(data.points)) {
        await update($, touchStatus, () => `Touch: sent a gesture of ${(data.points as unknown[]).length} points`)
        await sendInput($, { type: 'gesture', points: data.points })
      }
    } catch (error) {
      const reason = error instanceof Error ? error.message : String(error)
      await update($, touchStatus, () => `Touch: could not reach the helper (${reason})`)
    }

    return {}
  })

  on('command.run', { command: 'android' }, async $ => {
    await $.ui.open({ id: PANE, title: 'Android' })
    await refresh($)
    const count = (await read($, devices)).length
    if (count > 0 && !(await read($, isLive))) {
      const surfaces = await $.session.surfaces()
      await startLive($, surfaces[0] === 'terminal' ? 'file' : 'svg')
    }

    return { text: `Android pane opened: ${count} device${count === 1 ? '' : 's'} attached.` }
  }).catch(() => ({ text: 'android-emulator: could not open the Android pane.' }))

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const { Box, Button, Text } = $.ui.resolve(e)
    const list = await read($, devices)
    const current = await read($, selected)
    const shot = await read($, frame)
    const error = await read($, problem)
    const busy = await read($, isBusy)
    const live = await read($, isLive)
    const liveShot = await read($, liveFrame)
    const rate = await read($, liveRate)
    const liveTerminal = await read($, liveFile)
    const probing = await read($, isProbing)
    const touch = await read($, touchStatus)

    let screen = null
    if (live && liveTerminal !== null && e.surface === 'terminal') {
      const { Client, Image } = $.ui.resolve(e)
      const maxRows = Math.max(4, (e.viewport?.rows ?? 40) - PANE_CHROME_ROWS)
      const cells = fitCells(liveTerminal.width, liveTerminal.height, e.props.bodyColumns, maxRows)
      screen = (
        <Box position="relative" width={cells.columns} height={cells.rows}>
          <Image
            key="screen"
            source={{
              file: liveTerminal.path,
              format: 'rgb',
              width: liveTerminal.width,
              height: liveTerminal.height,
              generation: liveTerminal.seq,
            }}
            columns={cells.columns}
            rows={cells.rows}
            alt="Android screen (this terminal cannot draw images)"
          />
          <Box position="absolute" top={0} left={0} width={cells.columns} height={cells.rows}>
            <Client key="touch" module="./touch-overlay.tsx" width={cells.columns} height={cells.rows} />
          </Box>
        </Box>
      )
    } else if (live && liveShot !== null && e.surface !== 'terminal') {
      const display = displaySize(liveShot.width, liveShot.height)
      const svg = screenSvg(liveShot.base64, liveShot.width, liveShot.height)
      if (svg === null) {
        screen = <Text dimColor>The live frame is too large to draw here.</Text>
      } else if (e.surface === 'desktop') {
        const { Client, Svg } = $.ui.resolve(e)
        const cells = overlayCells(display, await read($, cellMetrics))
        screen = (
          <Box flexDirection="column" alignSelf="flex-start">
            <Box flexDirection="row">
              <Box position="relative">
                <Svg source={svg} alt="Android screen" width={display.width} height={display.height} />
                <Box position="absolute" top={0} left={0}>
                  <Client key="touch" module="./touch-overlay.tsx" width={cells.columns} height={cells.rows} />
                </Box>
              </Box>
              <Client key="ruler-y" module="./ruler.tsx" width={1} />
            </Box>
            <Client key="ruler-x" module="./ruler.tsx" width="100%" height={1} />
          </Box>
        )
      } else {
        const { Svg } = $.ui.resolve(e)
        screen = <Svg source={svg} alt="Android screen" width={display.width} height={display.height} />
      }
    } else if (shot !== null && e.surface === 'terminal') {
      const { Image } = $.ui.resolve(e)
      const maxRows = Math.max(4, (e.viewport?.rows ?? 40) - PANE_CHROME_ROWS)
      const cells = fitCells(shot.width, shot.height, e.props.bodyColumns, maxRows)
      screen = (
        <Image
          key="screen"
          source={{ file: shot.png, format: 'png', generation: shot.generation }}
          columns={cells.columns}
          rows={cells.rows}
          alt={`${shot.serial} screen (this terminal cannot draw images)`}
        />
      )
    } else if (shot !== null && e.surface !== 'terminal') {
      const { Svg } = $.ui.resolve(e)
      const scale = PREVIEW_LONG_SIDE / Math.max(shot.width, shot.height)
      const width = Math.round(shot.width * scale)
      const height = Math.round(shot.height * scale)
      const { base64 } = await $.fs.read(shot.jpeg, { as: 'bytes' })
      const svg = screenSvg(base64, width, height)
      const shown = Math.min(height, PREVIEW_MAX_HEIGHT)
      screen =
        svg === null ? (
          <Text dimColor>The screenshot is too large to draw here.</Text>
        ) : (
          <Svg
            source={svg}
            alt={`${shot.serial} screen`}
            width={Math.round((width * shown) / height)}
            height={shown}
          />
        )
    }

    let probe = null
    if (probing && e.surface !== 'terminal') {
      const { Markdown, Svg } = $.ui.resolve(e)
      probe = (
        <Box flexDirection="column" gap={1}>
          <Text bold>Stream test: which of these moves while you swipe?</Text>
          <Box flexDirection="row" flexWrap="wrap" gap={2}>
            {PROBE_VARIANTS.map(variant => (
              <Box key={`probe-${variant}`} flexDirection="column">
                <Text dimColor>{variant}</Text>
                <Svg
                  source={streamSvg(variant, 360, 800)}
                  alt={variant}
                  width={144}
                  height={320}
                  isInteractive={variant === 'svg-interactive'}
                />
              </Box>
            ))}
            <Box key="probe-markdown" flexDirection="column">
              <Text dimColor>markdown (stream, still)</Text>
              <Markdown text={streamMarkdown('markdown')} />
            </Box>
          </Box>
        </Box>
      )
    }

    return (
      <Box flexDirection="column" gap={1}>
        <Box flexDirection="row" flexWrap="wrap" gap={1}>
          {list.map(device => (
            <Button
              key={`device-${device.serial}`}
              variant={device.serial === current ? 'primary' : 'secondary'}
              onPress={() => select($, device.serial)}
            >
              {device.model ?? device.serial} <Text dimColor>{device.state}</Text>
            </Button>
          ))}
          <Button key="refresh" label={busy ? 'Refreshing' : 'Refresh'} onPress={() => refresh($)} />
          <Button
            key="live"
            variant={live ? 'primary' : 'secondary'}
            label={live ? `Stop live (${rate} fps)` : 'Go live'}
            onPress={press => (live ? stopLive($) : startLive($, press.surface === 'terminal' ? 'file' : 'svg'))}
          />
          {e.surface !== 'terminal' && (
            <Button
              key="probe"
              label={probing ? 'Hide stream test' : 'Test stream'}
              onPress={() => update($, isProbing, value => !value)}
            />
          )}
        </Box>
        {live && (
          <Box flexDirection="row" gap={1}>
            {NAV_KEYS.map(nav => (
              <Button
                key={`nav-${nav.key}`}
                label={nav.label}
                onPress={() => sendInput($, { type: 'key', key: nav.key })}
              />
            ))}
          </Box>
        )}
        {live && <Text dimColor>{touch ?? 'Touch: waiting for the overlay to report its size'}</Text>}
        {error !== null && <Text color="error">{error}</Text>}
        {list.length === 0 && (
          <Text dimColor>No devices attached. Start one (emulator -avd NAME), then press Refresh.</Text>
        )}
        {screen}
        {probe}
      </Box>
    )
  })
}
