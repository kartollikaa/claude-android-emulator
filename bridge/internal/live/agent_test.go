package live

import (
	"testing"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/emulator"
)

func TestScreenSizeFitsTheLongSide(t *testing.T) {
	for _, screen := range []struct{ upright, want emulator.Size }{
		{emulator.Size{Width: 1080, Height: 2400}, emulator.Size{Width: 576, Height: 1280}},
		{emulator.Size{Width: 2400, Height: 1080}, emulator.Size{Width: 1280, Height: 576}},
		{emulator.Size{Width: 8, Height: 16}, emulator.Size{Width: 8, Height: 16}},
	} {
		if got := screenSize(screen.upright); got != screen.want {
			t.Errorf("a %v screen is drawn at %v, want %v", screen.upright, got, screen.want)
		}
	}
}
