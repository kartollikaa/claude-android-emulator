import type { Device } from '../types'

export const SVG_SOURCE_LIMIT = 131072
const CELL_HEIGHT_TO_WIDTH = 2
const MAX_CELLS = 255

export const CAPTURE_SCRIPT =
  'mkdir -p "$(dirname "$2")" && exec "$0" -s "$1" exec-out screencap -p > "$2"'

export function adbCandidates(env: {
  androidHome?: string
  sdkRoot?: string
  home?: string
}): string[] {
  const roots = [
    env.androidHome,
    env.sdkRoot,
    env.home && `${env.home}/Library/Android/sdk`,
    env.home && `${env.home}/Android/Sdk`,
  ].filter((root): root is string => Boolean(root))

  return [
    ...roots.map(root => `${root}/platform-tools/adb`),
    '/opt/homebrew/bin/adb',
    '/usr/local/bin/adb',
  ]
}

export function parseDevices(stdout: string): Device[] {
  return stdout
    .split('\n')
    .slice(1)
    .map(line => line.trim())
    .filter(line => line.length > 0 && !line.startsWith('*'))
    .map(line => {
      const [serial = '', state = 'unknown', ...details] = line.split(/\s+/)
      const model = details
        .find(detail => detail.startsWith('model:'))
        ?.slice('model:'.length)
        .replaceAll('_', ' ')

      return { serial, state, model, isEmulator: serial.startsWith('emulator-') }
    })
}

export function parseSipsSize(stdout: string): { width: number; height: number } | null {
  const width = Number(/pixelWidth:\s*(\d+)/.exec(stdout)?.[1])
  const height = Number(/pixelHeight:\s*(\d+)/.exec(stdout)?.[1])

  return width > 0 && height > 0 ? { width, height } : null
}

export function fitCells(
  width: number,
  height: number,
  maxColumns: number,
  maxRows: number,
): { columns: number; rows: number } {
  const clamp = (cells: number) => Math.min(MAX_CELLS, Math.max(1, Math.round(cells)))
  const columnsAtFullHeight = (maxRows * width * CELL_HEIGHT_TO_WIDTH) / height

  if (columnsAtFullHeight <= maxColumns) {
    return { columns: clamp(columnsAtFullHeight), rows: clamp(maxRows) }
  }

  return {
    columns: clamp(maxColumns),
    rows: clamp((maxColumns * height) / (width * CELL_HEIGHT_TO_WIDTH)),
  }
}

export function screenSvg(jpegBase64: string, width: number, height: number): string | null {
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" ` +
    `width="${width}" height="${height}"><image width="${width}" height="${height}" ` +
    `href="data:image/jpeg;base64,${jpegBase64}"/></svg>`

  return svg.length <= SVG_SOURCE_LIMIT ? svg : null
}
