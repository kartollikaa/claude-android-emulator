// Package device is what the live view needs from the Android device it shows, whatever connects it.
package device

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

var (
	// ErrCannotShutDown is a phone's answer to Shutdown: only an emulator powers off from the live view.
	ErrCannotShutDown = errors.New("only an emulator shuts down from here; a phone keeps its own power button")
	// ErrUnknownKey is returned for a key name the device has no key for.
	ErrUnknownKey = errors.New("unknown key")
)

// Kind tells an emulator from a phone adb reaches.
type Kind string

const (
	Emulator Kind = "emulator"
	Phone    Kind = "device"
)

// Device is one connected Android device: its screen as a stream of frames, its input, and its lifecycle.
type Device interface {
	Serial() string
	Kind() Kind
	// Name is the AVD's name for an emulator, the model for a phone.
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

// Frame is one screen image drawn upright for the device's rotation: an emulator's RGB888, rows top-down, 0×0 when
// the display is off; or a phone's H.264 packet in Video, Width×Height then being the size of the video it belongs to.
type Frame struct {
	Seq      uint32
	Width    int
	Height   int
	Rotation Rotation
	RGB      []byte
	Video    *Packet
}

// Packet is one Annex B packet of a phone's video: a config packet (SPS and PPS), a key frame, or a frame that needs those before it.
type Packet struct {
	Data       []byte
	IsConfig   bool
	IsKeyFrame bool
}

// VideoResetter is a device whose video can start over: a config packet and a key frame follow soon after.
type VideoResetter interface {
	ResetVideo() error
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
