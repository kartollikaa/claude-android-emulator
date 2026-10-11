import type { FileFrame, LiveFrame } from '../types'

export const HELPER = 'http://127.0.0.1:7357'
export const LONG_POLL_MS = 1000
export const IDLE_MS = 30
export const RATE_WINDOW_MS = 1000

export function parseLiveFrame(text: string): LiveFrame | null {
  const parsed: unknown = JSON.parse(text)
  if (typeof parsed !== 'object' || parsed === null) return null
  const { seq, width, height, base64 } = parsed as Record<string, unknown>

  return typeof seq === 'number' &&
    typeof width === 'number' &&
    typeof height === 'number' &&
    typeof base64 === 'string'
    ? { seq, width, height, base64 }
    : null
}

export function parseFileFrame(text: string): FileFrame | null {
  const parsed: unknown = JSON.parse(text)
  if (typeof parsed !== 'object' || parsed === null) return null
  const { seq, width, height, path } = parsed as Record<string, unknown>

  return typeof seq === 'number' &&
    typeof width === 'number' &&
    typeof height === 'number' &&
    typeof path === 'string'
    ? { seq, width, height, path }
    : null
}

export const PROBE_VARIANTS = ['svg-interactive', 'svg-plain'] as const

export function streamSvg(variant: string, width: number, height: number): string {
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" width="${width}" height="${height}">` +
    `<image width="${width}" height="${height}" href="${HELPER}/stream.mjpeg?v=${variant}"/></svg>`
  )
}

export function streamMarkdown(variant: string): string {
  return `![${variant}](${HELPER}/stream.mjpeg?v=${variant}) ![${variant}-still](${HELPER}/frame.jpg?v=${variant}-still)`
}

export const DISPLAY_MAX_HEIGHT = 640

export function displaySize(frameWidth: number, frameHeight: number): { width: number; height: number } {
  const height = Math.min(frameHeight, DISPLAY_MAX_HEIGHT)

  return { width: Math.round((frameWidth * height) / frameHeight), height }
}

export type TouchEvent = {
  seq: number
  type: 'down' | 'move' | 'up' | 'enter' | 'leave'
  x: number
  y: number
  fine?: { x: number; y: number }
}

const TOUCH_EVENT_TYPES = new Set(['down', 'move', 'up', 'enter', 'leave'])

export function parseTouchEvents(value: unknown): TouchEvent[] {
  if (!Array.isArray(value)) return []

  return value.flatMap(item => {
    if (typeof item !== 'object' || item === null) return []
    const { seq, type, x, y, fine } = item as Record<string, unknown>
    if (typeof seq !== 'number' || typeof type !== 'string' || !TOUCH_EVENT_TYPES.has(type)) return []
    if (typeof x !== 'number' || typeof y !== 'number') return []
    const at = fine as { x?: unknown; y?: unknown } | undefined
    const hasFine = typeof at?.x === 'number' && typeof at.y === 'number'

    return [{ seq, type: type as TouchEvent['type'], x, y, ...(hasFine ? { fine: { x: at.x as number, y: at.y as number } } : {}) }]
  })
}

const clampUnit = (value: number) => Math.min(1, Math.max(0, value))

// The region's cells are floored from its pixels: half a cell stands in for the lost fraction.
export function touchPoint(
  event: Pick<TouchEvent, 'x' | 'y' | 'fine'>,
  region: { columns: number; rows: number },
): { x: number; y: number } {
  return {
    x: clampUnit((event.fine?.x ?? event.x + 0.5) / (region.columns + 0.5)),
    y: clampUnit((event.fine?.y ?? event.y + 0.5) / (region.rows + 0.5)),
  }
}
