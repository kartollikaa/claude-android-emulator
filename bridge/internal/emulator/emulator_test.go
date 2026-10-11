package emulator

import "testing"

func TestToPixelKeepsTheEdgesOnScreen(t *testing.T) {
	for _, step := range []struct {
		fraction float64
		extent   int
		want     int32
	}{
		{0, 1080, 0},
		{1, 1080, 1079},
		{0.5, 8, 4},
		{1.2, 2400, 2399},
		{-0.1, 2400, 0},
	} {
		if got := toPixel(step.fraction, step.extent); got != step.want {
			t.Errorf("toPixel(%v, %d) = %d, want %d", step.fraction, step.extent, got, step.want)
		}
	}
}

func TestStreamSizeKeepsTheScreenAspect(t *testing.T) {
	conn := &Conn{Device: Size{Width: 1080, Height: 2400}}

	if got, want := conn.StreamSize(540), (Size{Width: 540, Height: 1200}); got != want {
		t.Errorf("StreamSize(540) = %+v, want %+v", got, want)
	}
	if got, want := conn.StreamSize(4000), (Size{Width: 1080, Height: 2400}); got != want {
		t.Errorf("StreamSize(4000) = %+v, want the native %+v", got, want)
	}
}
