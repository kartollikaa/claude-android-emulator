package emulator

import (
	"testing"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/device"
)

func TestStreamBoxFitsBothOrientations(t *testing.T) {
	phone := &Conn{panel: device.Size{Width: 1080, Height: 2400}}
	if got, want := phone.streamBox(540), (device.Size{Width: 1200, Height: 1200}); got != want {
		t.Errorf("streamBox(540) = %+v, want %+v: 540 wide upright, 540 tall on its side", got, want)
	}
	if got, want := phone.streamBox(4000), (device.Size{Width: 2400, Height: 2400}); got != want {
		t.Errorf("streamBox(4000) = %+v, want the native long side %+v", got, want)
	}
	tablet := &Conn{panel: device.Size{Width: 2560, Height: 1600}}
	if got, want := tablet.streamBox(800), (device.Size{Width: 1280, Height: 1280}); got != want {
		t.Errorf("tablet streamBox(800) = %+v, want %+v", got, want)
	}
}

func TestTouchFollowsTheRotation(t *testing.T) {
	for _, step := range []struct {
		rotation     device.Rotation
		x, y         float64
		wantX, wantY float64
	}{
		{device.Portrait, 0.25, 0.75, 0.25, 0.75},
		{device.Landscape, 0.25, 0.75, 0.25, 0.25},
		{device.ReversePortrait, 0.25, 0.75, 0.75, 0.25},
		{device.ReverseLandscape, 0.25, 0.75, 0.75, 0.75},
		{device.Landscape, 0, 0, 1, 0},
		{device.ReverseLandscape, 0, 0, 0, 1},
	} {
		if x, y := toPanel(step.rotation, step.x, step.y); x != step.wantX || y != step.wantY {
			t.Errorf("%v: (%v, %v) on screen = (%v, %v) on the panel, want (%v, %v)", step.rotation, step.x, step.y, x, y, step.wantX, step.wantY)
		}
	}
}
