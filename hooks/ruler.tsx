import type { ClientModule } from 'claude-code'

const MEASURE_MS = 500

const Ruler: ClientModule<null, boolean> = (_props, surface) => {
  if (surface.state === undefined) {
    let reported = ''
    surface.every(MEASURE_MS, () => {
      const size = `${surface.columns}x${surface.rows}`
      if (surface.columns > 0 && surface.rows > 0 && size !== reported) {
        reported = size
        surface.post({ type: 'ruler', columns: surface.columns, rows: surface.rows })
      }
    })
    surface.setState(true)
  }

  const { Box } = surface.elements

  return <Box width="100%" height="100%" />
}

export default Ruler
