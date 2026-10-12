package device

import "testing"

func TestToPixel(t *testing.T) {
	for _, step := range []struct {
		fraction float64
		extent   int
		want     int32
	}{
		{0, 536, 0}, {1, 536, 535}, {0.5, 536, 268}, {0.25, 1208, 302}, {-0.2, 536, 0}, {1.7, 536, 535},
	} {
		if got := ToPixel(step.fraction, step.extent); got != step.want {
			t.Errorf("ToPixel(%v, %d) = %d, want %d", step.fraction, step.extent, got, step.want)
		}
	}
}
