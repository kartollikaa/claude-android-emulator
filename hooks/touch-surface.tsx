import type { ClientModule } from 'claude-code'

type KeptEvent = {
  seq: number
  type: string
  x: number
  y: number
  fine?: { x: number; y: number }
}

// A post replaces an undelivered one from the same frame, so each post resends the recent events.
const KEPT_EVENTS = 24
const SIZE_REPORT_MS = 250
// The desktop region shrinks to its Client's height but never grows to it: draw taller than any picture.
const OVERFLOW_ROWS = 200

const TouchSurface: ClientModule<null, boolean> = (_props, surface) => {
  if (surface.state === undefined) {
    const epoch = Date.now()
    let seq = 0
    let kept: KeptEvent[] = []
    let reported = ''
    const report = () =>
      surface.post({ type: 'touch', epoch, columns: surface.columns, rows: surface.rows, events: kept })

    surface.every(SIZE_REPORT_MS, () => {
      const size = `${surface.columns}x${surface.rows}`
      if (surface.columns > 0 && surface.rows > 0 && size !== reported) {
        reported = size
        report()
      }
    })

    surface.onPointer(event => {
      if (event.type === 'move' && event.button === undefined) return
      seq += 1
      kept = [
        ...kept,
        { seq, type: event.type, x: event.x, y: event.y, ...(event.fine === undefined ? {} : { fine: event.fine }) },
      ].slice(-KEPT_EVENTS)
      report()
    })
    surface.setState(true)
  }

  const { Box } = surface.elements

  return <Box width="100%" height={OVERFLOW_ROWS} />
}

export default TouchSurface
