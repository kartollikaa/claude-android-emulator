import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

import grpc from '@grpc/grpc-js'
import protoLoader from '@grpc/proto-loader'

const SECONDS = Number(process.env.SECONDS ?? 6)
const WIDTH = Number(process.env.WIDTH ?? 540)
const FORMAT = process.env.FORMAT ?? 'RGB888'
const SDK = process.env.ANDROID_HOME ?? path.join(os.homedir(), 'Library/Android/sdk')
const DISCOVERY = path.join(os.homedir(), 'Library/Caches/TemporaryItems/avd/running')

const ini = fs.readdirSync(DISCOVERY).find(f => /^pid_\d+\.ini$/.test(f))
const entries = Object.fromEntries(
  fs.readFileSync(path.join(DISCOVERY, ini), 'utf8').split('\n').map(l => l.split(/=(.*)/s).slice(0, 2)),
)
const definition = protoLoader.loadSync(path.join(SDK, 'emulator/lib/emulator_controller.proto'), {
  keepCase: true, longs: Number, enums: String, defaults: true,
})
const { EmulatorController } = grpc.loadPackageDefinition(definition).android.emulation.control
const controller = new EmulatorController(`localhost:${entries['grpc.port']}`, grpc.credentials.createInsecure(), {
  'grpc.max_receive_message_length': 64 * 1024 * 1024,
})
const metadata = new grpc.Metadata()
if (entries['grpc.token']) metadata.set('authorization', `Bearer ${entries['grpc.token']}`)
const call = (method, request) =>
  new Promise((resolve, reject) => controller[method](request, metadata, (e, r) => (e ? reject(e) : resolve(r))))

const native = (await call('getScreenshot', { format: 'PNG' })).format
const height = Math.round((WIDTH * native.height) / native.width)
const sleep = ms => new Promise(r => setTimeout(r, ms))

let frames = 0
let bytes = 0
const perSecond = []
const stream = controller.streamScreenshot({ format: FORMAT, width: WIDTH, height }, metadata)
stream.on('data', image => { frames += 1; bytes += image.image.length })
stream.on('error', () => {})
const ticker = setInterval(() => { perSecond.push(frames); frames = 0 }, 1000)

let isDriving = true
async function drive() {
  const x = Math.round(native.width / 2)
  while (isDriving) {
    for (const [from, to] of [[0.85, 0.25], [0.3, 0.9]]) {
      for (let step = 0; step <= 20 && isDriving; step++) {
        const y = Math.round(native.height * (from + ((to - from) * step) / 20))
        await call('sendTouch', { touches: [{ x, y, identifier: 0, pressure: 1 }] })
        await sleep(16)
      }
      await call('sendTouch', { touches: [{ x, y: Math.round(native.height * to), identifier: 0, pressure: 0 }] })
      await sleep(400)
    }
  }
}
const driving = drive()
await sleep(SECONDS * 1000)
isDriving = false
await driving
clearInterval(ticker)
stream.cancel()
const average = perSecond.reduce((a, b) => a + b, 0) / perSecond.length
console.log(JSON.stringify({ format: FORMAT, size: `${WIDTH}x${height}`, perSecond, average: Math.round(average * 10) / 10, mbPerFrame: Math.round((bytes / Math.max(1, perSecond.reduce((a, b) => a + b, 0))) / 1e4) / 100 }))
process.exit(0)
