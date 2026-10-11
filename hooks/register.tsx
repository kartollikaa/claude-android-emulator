import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import {
  CAPTURE_SCRIPT,
  adbCandidates,
  fitCells,
  parseDevices,
  parseSipsSize,
  screenSvg,
} from './adb'

const PANE = 'android'
const PREVIEW_LONG_SIDE = 720
const PREVIEW_MAX_HEIGHT = 640
const PANE_CHROME_ROWS = 6

const devices = atom({ plugin: 'android-emulator', key: 'devices' } as const, [])
const selected = atom({ plugin: 'android-emulator', key: 'selected' } as const, null)
const frame = atom({ plugin: 'android-emulator', key: 'frame' } as const, null)
const problem = atom({ plugin: 'android-emulator', key: 'problem' } as const, null)
const isBusy = atom({ plugin: 'android-emulator', key: 'isBusy' } as const, false)

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

    return next(e)
  })

  on('command.run', { command: 'android' }, async $ => {
    await $.ui.open({ id: PANE, title: 'Android' })
    await refresh($)
    const count = (await read($, devices)).length

    return { text: `Android pane opened: ${count} device${count === 1 ? '' : 's'} attached.` }
  }).catch(() => ({ text: 'android-emulator: could not open the Android pane.' }))

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const { Box, Button, Text } = $.ui.resolve(e)
    const list = await read($, devices)
    const current = await read($, selected)
    const shot = await read($, frame)
    const error = await read($, problem)
    const busy = await read($, isBusy)

    let screen = null
    if (shot !== null && e.surface === 'terminal') {
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
        </Box>
        {error !== null && <Text color="error">{error}</Text>}
        {list.length === 0 && (
          <Text dimColor>No devices attached. Start one (emulator -avd NAME), then press Refresh.</Text>
        )}
        {screen}
      </Box>
    )
  })
}
