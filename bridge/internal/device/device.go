// Package device is what the live view needs from the Android device it shows, whatever connects it.
package device

import (
	"context"
	"fmt"
	"slices"
)

// Device is one connected Android device: its screen as a stream of frames, its input, and its lifecycle.
type Device interface {
	Serial() string
	// Name is the AVD's name for an emulator.
	Name() string
	// Panel is the native portrait panel size, whatever the rotation.
	Panel() Size
	// Stream calls onFrame for each new screen image, streamed with a short side of width, until ctx ends or the device goes away.
	Stream(ctx context.Context, width int, onFrame func(Frame)) error
	// Touch presses (isDown) or releases one finger at a point given as fractions of the upright screen.
	Touch(ctx context.Context, x, y float64, isDown bool) error
	// Key presses and releases a named key (GoHome, GoBack, AppSwitch, Enter, Backspace, ...).
	Key(ctx context.Context, key string) error
	Text(ctx context.Context, text string) error
	// Rotate turns the device quarters of a turn, counter-clockwise for a positive count.
	Rotate(ctx context.Context, quarters int) error
	// Screenshot returns the screen as a PNG at the device's full resolution.
	Screenshot(ctx context.Context) ([]byte, error)
	// ScreenshotAt returns the screen upright, scaled to fit size.
	ScreenshotAt(ctx context.Context, size Size) (Frame, error)
	// Upright is the screen's size as the newest frame shows it: the panel, turned with the device.
	Upright() Size
	Booted(ctx context.Context) (bool, error)
	Shutdown(ctx context.Context) error
	Close() error
}

type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Frame is one screen image: RGB888, rows top-down, drawn upright for the device's rotation; 0×0 means the display is off.
type Frame struct {
	Seq      uint32
	Width    int
	Height   int
	Rotation Rotation
	RGB      []byte
}

// Rotation is how far the device is turned counter-clockwise from its natural portrait, in quarter turns.
type Rotation int32

const (
	Portrait Rotation = iota
	Landscape
	ReversePortrait
	ReverseLandscape
)

var rotationNames = [...]string{"portrait", "landscape", "reverse-portrait", "reverse-landscape"}

func (r Rotation) MarshalText() ([]byte, error) { return []byte(rotationNames[r&3]), nil }

func (r *Rotation) UnmarshalText(text []byte) error {
	index := slices.Index(rotationNames[:], string(text))
	if index < 0 {
		return fmt.Errorf("unknown rotation %q", text)
	}
	*r = Rotation(index)

	return nil
}
