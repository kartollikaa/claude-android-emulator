import type { CellMetrics, FileFrame, LiveFrame } from '../types'

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
export const DEFAULT_CELL_METRICS: CellMetrics = { advance: 8, lineHeight: 16 }

export function displaySize(frameWidth: number, frameHeight: number): { width: number; height: number } {
  const height = Math.min(frameHeight, DISPLAY_MAX_HEIGHT)

  return { width: Math.round((frameWidth * height) / frameHeight), height }
}

export function overlayCells(
  display: { width: number; height: number },
  metrics: CellMetrics,
): { columns: number; rows: number } {
  return {
    columns: Math.max(1, Math.round(display.width / metrics.advance)),
    rows: Math.max(1, Math.round(display.height / metrics.lineHeight)),
  }
}

export function measuredMetrics(
  metrics: CellMetrics,
  ruler: 'ruler-x' | 'ruler-y',
  display: { width: number; height: number },
  cells: { columns: number; rows: number },
): CellMetrics {
  if (ruler === 'ruler-x' && cells.columns > 1) {
    return { ...metrics, advance: display.width / cells.columns }
  }
  if (ruler === 'ruler-y' && cells.rows > 1) {
    return { ...metrics, lineHeight: display.height / cells.rows }
  }

  return metrics
}
