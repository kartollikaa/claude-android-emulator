import type { FileFrame, LiveFrame } from '../types'

export const HELPER = 'http://127.0.0.1:7357'
export const POLL_MS = 100
export const FILE_POLL_MS = 25
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
