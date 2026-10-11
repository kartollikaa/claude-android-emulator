import fs from 'node:fs'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import grpc from '@grpc/grpc-js'
import protoLoader from '@grpc/proto-loader'
import jpeg from 'jpeg-js'
import { WebSocketServer } from 'ws'

const PORT = Number(process.env.PORT ?? 7357)
const STREAM_WIDTH = Number(process.env.STREAM_WIDTH ?? 540)
const PANE_WIDTH = Number(process.env.PANE_WIDTH ?? 360)
const SDK = process.env.ANDROID_HOME ?? path.join(os.homedir(), 'Library/Android/sdk')
const DISCOVERY = path.join(os.homedir(), 'Library/Caches/TemporaryItems/avd/running')
const HERE = path.dirname(fileURLToPath(import.meta.url))
const TOUCH_PRESSURE = 1
const FILE_SLOTS = 8
const FILE_DIRECTORY = path.join(os.tmpdir(), 'claude-android-emulator', 'live')
const TAP_MAX_TRAVEL = 0.02
const TAP_MAX_MS = 300

function discoverEmulator() {
  const files = fs.existsSync(DISCOVERY) ? fs.readdirSync(DISCOVERY).filter(f => /^pid_\d+\.ini$/.test(f)) : []
  for (const file of files) {
    const entries = Object.fromEntries(
      fs.readFileSync(path.join(DISCOVERY, file), 'utf8')
        .split('\n')
        .map(line => line.split(/=(.*)/s).slice(0, 2))
        .filter(([key]) => key),
    )
    if (entries['grpc.port']) {
      return { port: entries['grpc.port'], token: entries['grpc.token'], serial: `emulator-${entries['port.serial']}` }
    }
  }
  throw new Error(`no running emulator with gRPC found in ${DISCOVERY}`)
}

const emulator = discoverEmulator()
const definition = protoLoader.loadSync(path.join(SDK, 'emulator/lib/emulator_controller.proto'), {
  keepCase: true,
  longs: Number,
  enums: String,
  defaults: true,
})
const { EmulatorController } = grpc.loadPackageDefinition(definition).android.emulation.control
const controller = new EmulatorController(`localhost:${emulator.port}`, grpc.credentials.createInsecure(), {
  'grpc.max_receive_message_length': 64 * 1024 * 1024,
})
const metadata = new grpc.Metadata()
if (emulator.token) metadata.set('authorization', `Bearer ${emulator.token}`)

const call = (method, request) =>
  new Promise((resolve, reject) =>
    controller[method](request, metadata, (error, reply) => (error ? reject(error) : resolve(reply))),
  )

const native = (await call('getScreenshot', { format: 'PNG', width: 0, height: 0 })).format
const device = { width: native.width, height: native.height }
const stream = { width: STREAM_WIDTH, height: Math.round((STREAM_WIDTH * device.height) / device.width) }
console.log(`emulator ${emulator.serial} on grpc :${emulator.port}, screen ${device.width}x${device.height}, streaming ${stream.width}x${stream.height}`)

let latest = null
let framesSinceReport = 0
const polls = { frame: 0, 'frame-file': 0 }
const sockets = new Set()
const mjpegClients = new Set()
const BOUNDARY = 'androidframe'

function framePacket(frame) {
  const header = Buffer.alloc(12)
  header.writeUInt32LE(frame.width, 0)
  header.writeUInt32LE(frame.height, 4)
  header.writeUInt32LE(frame.seq, 8)
  return Buffer.concat([header, frame.rgb])
}

function startStream() {
  const frames = controller.streamScreenshot({ format: 'RGB888', width: stream.width, height: stream.height }, metadata)
  frames.on('data', image => {
    latest = { seq: image.seq, width: image.format.width, height: image.format.height, rgb: image.image, jpeg: null }
    framesSinceReport += 1
    if (mjpegClients.size > 0) pushMjpeg(latest)
    if (sockets.size === 0) return
    const packet = framePacket(latest)
    for (const socket of sockets) if (socket.bufferedAmount < 8 * 1024 * 1024) socket.send(packet)
  })
  frames.on('error', error => {
    console.error(`stream ended: ${error.message}; retrying`)
    setTimeout(startStream, 1000)
  })
}
startStream()
setInterval(() => {
  const polled = Object.entries(polls).filter(([, count]) => count > 0)
  if (framesSinceReport > 1 || polled.length > 0) {
    console.log(`${framesSinceReport} frames/s from the emulator; pane polls/s: ${polled.map(([k, v]) => `${k}=${v}`).join(' ') || 'none'}`)
  }
  framesSinceReport = 0
  for (const key of Object.keys(polls)) polls[key] = 0
}, 1000)

function paneJpeg(frame) {
  if (frame.jpeg) return frame.jpeg
  const width = Math.min(PANE_WIDTH, frame.width)
  const height = Math.round((width * frame.height) / frame.width)
  const rgba = Buffer.alloc(width * height * 4)
  for (let y = 0; y < height; y++) {
    const sy = Math.floor((y * frame.height) / height)
    for (let x = 0; x < width; x++) {
      const sx = Math.floor((x * frame.width) / width)
      const from = (sy * frame.width + sx) * 3
      const to = (y * width + x) * 4
      rgba[to] = frame.rgb[from]
      rgba[to + 1] = frame.rgb[from + 1]
      rgba[to + 2] = frame.rgb[from + 2]
      rgba[to + 3] = 255
    }
  }
  frame.jpeg = { width, height, base64: jpeg.encode({ data: rgba, width, height }, 60).data.toString('base64') }
  return frame.jpeg
}

function frameFile(frame) {
  if (frame.file) return frame.file
  fs.mkdirSync(FILE_DIRECTORY, { recursive: true })
  const file = path.join(FILE_DIRECTORY, `slot-${frame.seq % FILE_SLOTS}.rgb`)
  fs.writeFileSync(file, frame.rgb)
  frame.file = file
  return file
}

function pushMjpeg(frame) {
  const { base64 } = paneJpeg(frame)
  const body = Buffer.from(base64, 'base64')
  const part = Buffer.concat([
    Buffer.from(`Content-Type: image/jpeg\r\nContent-Length: ${body.length}\r\n\r\n`),
    body,
    Buffer.from(`\r\n--${BOUNDARY}\r\n`),
  ])
  for (const client of mjpegClients) if (client.writableLength < 4 * 1024 * 1024) client.write(part)
}

const toDevice = ({ x, y }) => ({
  x: Math.round(Math.min(1, Math.max(0, x)) * (device.width - 1)),
  y: Math.round(Math.min(1, Math.max(0, y)) * (device.height - 1)),
})

function touch(point, isDown) {
  const at = toDevice(point)
  return call('sendTouch', { touches: [{ ...at, identifier: 0, pressure: isDown ? TOUCH_PRESSURE : 0 }] })
}

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))

async function gesture(points) {
  const first = points[0]
  const last = points[points.length - 1]
  if (!first || !last) return
  const travel = Math.hypot(last.x - first.x, last.y - first.y)
  if (travel < TAP_MAX_TRAVEL && last.t - first.t < TAP_MAX_MS) {
    await touch(first, true)
    await sleep(40)
    await touch(first, false)
    return
  }
  let clock = first.t
  for (const point of points) {
    await sleep(Math.min(50, Math.max(0, point.t - clock)))
    clock = point.t
    await touch(point, true)
  }
  await touch(last, false)
}

async function handle(message) {
  if (message.type === 'debug') {
    console.log(`pane debug: ${JSON.stringify(message)}`)
    return
  }
  console.log(`input: ${message.type}${message.key ? ` ${message.key}` : ''}${message.points ? ` ${message.points.length} points from ${JSON.stringify(message.points[0])} to ${JSON.stringify(message.points[message.points.length - 1])}` : ''}`)
  if (message.type === 'touch') return touch(message, message.phase !== 'up')
  if (message.type === 'gesture') return gesture(message.points ?? [])
  if (message.type === 'key') return call('sendKey', { eventType: 'keypress', key: message.key })
  if (message.type === 'text') return call('sendKey', { eventType: 'keypress', text: message.text })
}

const readBody = request =>
  new Promise(resolve => {
    let body = ''
    request.on('data', chunk => (body += chunk))
    request.on('end', () => resolve(body))
  })

const server = http.createServer(async (request, response) => {
  const url = new URL(request.url, `http://${request.headers.host}`)
  try {
    if (request.method === 'GET' && url.pathname === '/') {
      response.writeHead(200, { 'content-type': 'text/html; charset=utf-8' })
      response.end(fs.readFileSync(path.join(HERE, 'public/index.html')))
    } else if (request.method === 'GET' && url.pathname === '/stream.mjpeg') {
      console.log(`mjpeg stream opened: v=${url.searchParams.get('v')} ua=${request.headers['user-agent']}`)
      response.writeHead(200, {
        'content-type': `multipart/x-mixed-replace; boundary=${BOUNDARY}`,
        'cache-control': 'no-store',
        'access-control-allow-origin': '*',
      })
      response.write(`--${BOUNDARY}\r\n`)
      mjpegClients.add(response)
      if (latest !== null) pushMjpeg(latest)
      request.on('close', () => {
        mjpegClients.delete(response)
        console.log(`mjpeg stream closed: v=${url.searchParams.get('v')}`)
      })
    } else if (request.method === 'GET' && url.pathname === '/frame.jpg') {
      console.log(`jpeg requested: v=${url.searchParams.get('v')} ua=${request.headers['user-agent']}`)
      response.writeHead(200, { 'content-type': 'image/jpeg', 'cache-control': 'no-store', 'access-control-allow-origin': '*' })
      response.end(latest === null ? Buffer.alloc(0) : Buffer.from(paneJpeg(latest).base64, 'base64'))
    } else if (request.method === 'GET' && url.pathname === '/frame-file') {
      polls['frame-file'] += 1
      const after = Number(url.searchParams.get('after') ?? -1)
      response.writeHead(200, { 'content-type': 'application/json' })
      if (latest === null || latest.seq === after) {
        response.end(JSON.stringify({ seq: latest?.seq ?? -1 }))
      } else {
        response.end(JSON.stringify({ seq: latest.seq, width: latest.width, height: latest.height, path: frameFile(latest) }))
      }
    } else if (request.method === 'GET' && url.pathname === '/frame') {
      polls.frame += 1
      const after = Number(url.searchParams.get('after') ?? -1)
      response.writeHead(200, { 'content-type': 'application/json' })
      if (latest === null || latest.seq === after) {
        response.end(JSON.stringify({ seq: latest?.seq ?? -1 }))
      } else {
        response.end(JSON.stringify({ seq: latest.seq, ...paneJpeg(latest) }))
      }
    } else if (request.method === 'GET' && url.pathname === '/status') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end(JSON.stringify({ serial: emulator.serial, device, stream, seq: latest?.seq ?? -1 }))
    } else if (request.method === 'POST' && url.pathname === '/input') {
      await handle(JSON.parse(await readBody(request)))
      response.writeHead(204).end()
    } else {
      response.writeHead(404).end()
    }
  } catch (error) {
    response.writeHead(500, { 'content-type': 'text/plain' }).end(String(error?.message ?? error))
  }
})

new WebSocketServer({ server, path: '/live' }).on('connection', socket => {
  sockets.add(socket)
  if (latest !== null) socket.send(framePacket(latest))
  socket.on('message', raw => handle(JSON.parse(String(raw))).catch(error => console.error(error.message)))
  socket.on('close', () => sockets.delete(socket))
})

server.listen(PORT, '127.0.0.1', () => console.log(`live view on http://127.0.0.1:${PORT}`))
