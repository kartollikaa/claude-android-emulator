import type { ClientModule } from 'claude-code'

type Point = { x: number; y: number; t: number }

const SIZE_REPORT_MS = 250

const TouchOverlay: ClientModule<null, boolean> = (_props, surface) => {
  if (surface.state === undefined) {
    let path: Point[] = []
    let downAt = 0
    const fraction = (cell: number, fine: number | undefined, cells: number) =>
      Math.min(1, Math.max(0, (fine ?? cell + 0.5) / Math.max(1, cells)))

    const stopSizeReport = surface.every(SIZE_REPORT_MS, () => {
      if (surface.columns > 0 && surface.rows > 0) {
        surface.post({ type: 'overlay', columns: surface.columns, rows: surface.rows })
        stopSizeReport()
      }
    })

    surface.onPointer(event => {
      const point = {
        x: fraction(event.x, event.fine?.x, surface.columns),
        y: fraction(event.y, event.fine?.y, surface.rows),
      }
      if (surface.columns === 0 || surface.rows === 0) {
        if (event.type === 'down') surface.post({ type: 'pointer', phase: 'down', columns: 0, rows: 0 })
        return
      }
      if (event.type === 'down') {
        downAt = Date.now()
        path = [{ ...point, t: 0 }]
        surface.post({ type: 'pointer', phase: 'down', ...point, columns: surface.columns, rows: surface.rows })
      } else if (event.type === 'move' && path.length > 0 && event.button !== undefined) {
        path.push({ ...point, t: Date.now() - downAt })
      } else if (event.type === 'up' && path.length > 0) {
        path.push({ ...point, t: Date.now() - downAt })
        surface.post({ type: 'gesture', points: path })
        path = []
      }
    })
    surface.setState(true)
  }

  const { Box } = surface.elements

  return <Box width="100%" height="100%" />
}

export default TouchOverlay
