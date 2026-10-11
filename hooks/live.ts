import type { LiveFrame } from '../types'

export const HELPER = 'http://127.0.0.1:7357'
export const POLL_MS = 150
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
