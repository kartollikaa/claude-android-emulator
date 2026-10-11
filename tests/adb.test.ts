import { describe, expect, test } from 'claude-code/testing'

import { adbCandidates, fitCells, parseDevices, parseSipsSize, screenSvg } from '../hooks/adb'

describe('parseDevices', () => {
  test('reads emulators, USB devices and their states', () => {
    const stdout = [
      'List of devices attached',
      'emulator-5554          device product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a transport_id:1',
      'R58M1234ABC            unauthorized usb:1-1 transport_id:2',
      '',
    ].join('\n')

    expect(parseDevices(stdout)).toEqual([
      { serial: 'emulator-5554', state: 'device', model: 'sdk gphone64 arm64', isEmulator: true },
      { serial: 'R58M1234ABC', state: 'unauthorized', model: undefined, isEmulator: false },
    ])
  })

  test('skips daemon start-up lines and an empty list', () => {
    const stdout = [
      '* daemon not running; starting now at tcp:5037',
      '* daemon started successfully',
      'List of devices attached',
      '',
    ].join('\n')

    expect(parseDevices(stdout.split('\n').slice(2).join('\n'))).toEqual([])
    expect(parseDevices('List of devices attached\n* daemon started successfully\n')).toEqual([])
  })
})

describe('adbCandidates', () => {
  test('prefers the SDK named by the environment, then the default SDK, then Homebrew', () => {
    expect(adbCandidates({ androidHome: '/sdk', home: '/Users/me' })).toEqual([
      '/sdk/platform-tools/adb',
      '/Users/me/Library/Android/sdk/platform-tools/adb',
      '/Users/me/Android/Sdk/platform-tools/adb',
      '/opt/homebrew/bin/adb',
      '/usr/local/bin/adb',
    ])
  })
})

describe('parseSipsSize', () => {
  test('reads the pixel size sips prints', () => {
    expect(parseSipsSize('/tmp/screen.png\n  pixelWidth: 1080\n  pixelHeight: 2400\n')).toEqual({
      width: 1080,
      height: 2400,
    })
    expect(parseSipsSize('Error: no such file')).toBeNull()
  })
})

describe('fitCells', () => {
  test('a portrait screen fills the rows and keeps its aspect in double-height cells', () => {
    expect(fitCells(1080, 2400, 120, 40)).toEqual({ columns: 36, rows: 40 })
  })

  test('a narrow pane limits the width and shrinks the rows to match', () => {
    expect(fitCells(2400, 1080, 60, 40)).toEqual({ columns: 60, rows: 14 })
  })

  test('never leaves the 1-255 cell range', () => {
    expect(fitCells(100, 100, 1000, 1000)).toEqual({ columns: 255, rows: 255 })
  })
})

describe('screenSvg', () => {
  test('embeds the JPEG as a data URI sized to the frame', () => {
    const svg = screenSvg('AAAA', 360, 800)

    expect(svg).toContain('viewBox="0 0 360 800"')
    expect(svg).toContain('href="data:image/jpeg;base64,AAAA"')
  })

  test('refuses a frame past the Svg source limit', () => {
    expect(screenSvg('A'.repeat(140_000), 360, 800)).toBeNull()
  })
})
