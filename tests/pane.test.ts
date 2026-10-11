import { expect, test } from 'claude-code/testing'
import type { On } from 'claude-code'

const PANE = {
  plugin: 'android-emulator',
  component: 'Pane',
  requestId: 'android',
  props: {
    title: 'Android',
    isFocused: true,
    bodyColumns: 100,
    placement: 'dock',
    scroll: { offset: 0, bodyRows: 40 },
    view: {},
  },
} as const

const DEVICES = [
  'List of devices attached',
  'emulator-5554          device product:sdk_gphone64_arm64 model:Pixel_8 device:emu64a transport_id:1',
  '',
].join('\n')

function fakeHost(on: On, ran: string[][]) {
  on('env.get', () => ({ value: undefined }))
  on('fs.exists', () => ({ value: false }))
  on('fs.read', () => ({ value: { base64: 'AAAA' } }))
  on('process.run', ($, e) => {
    ran.push([...e.argv])
    const [command, ...args] = e.argv
    const stdout =
      command === 'adb' && args[0] === 'devices'
        ? DEVICES
        : command === 'sips' && args[0] === '-g'
          ? 'screen.png\n  pixelWidth: 1080\n  pixelHeight: 2400\n'
          : ''

    return {
      value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false },
    }
  })
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: an empty pane says how to attach a device`, async $ => {
    const ui = await $.ui.mount({ ...PANE, surface })

    expect(await ui.find({ type: 'Text', text: /No devices attached/ })).toBeDefined()
    expect(await ui.find({ key: 'refresh' })).toBeDefined()
    await ui.unmount()
  })

  test(`${surface}: Refresh lists the device and draws its screen`, async ($, on) => {
    const ran: string[][] = []
    fakeHost(on, ran)
    const ui = await $.ui.mount({ ...PANE, surface })

    await ui.press({ key: 'refresh' })

    expect(await ui.find({ key: 'device-emulator-5554', text: /Pixel 8/ })).toBeDefined()
    expect(await ui.find({ type: surface === 'terminal' ? 'Image' : 'Svg' })).toBeDefined()
    expect(ran.some(argv => argv[0] === 'sh' && argv.includes('emulator-5554'))).toBe(true)
    await ui.unmount()
  })
}
