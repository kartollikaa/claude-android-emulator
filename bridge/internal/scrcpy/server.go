// Package scrcpy speaks the pinned scrcpy server's protocol: its video packets, the SPS inside them and its control messages.
package scrcpy

import (
	_ "embed"
	"fmt"
	"strings"
)

// Version is the embedded server's release; the server refuses a client that names another.
const Version = "3.3.3"

// Server is the scrcpy-server release asset (Apache-2.0, LICENSE beside it), run on the device with app_process.
//
//go:embed scrcpy-server-v3.3.3.jar
var Server []byte

// DevicePath is where the server is pushed, its name carrying the version so another release never runs by mistake.
const DevicePath = "/data/local/tmp/claude-android-emulator-scrcpy-server-v" + Version + ".jar"

// Options are the server's settings this bridge varies.
type Options struct {
	// SCID names the server's abstract socket, scrcpy_<scid>: 31 random bits as 8 hex digits.
	SCID uint32
	// MaxSize caps the video's long side, in pixels.
	MaxSize int
	// BitRate is the encoder's target in bits per second; 0 keeps the server's default.
	BitRate int
}

// SocketName is the abstract socket the server listens on, the one adb forward reaches.
func (o Options) SocketName() string { return fmt.Sprintf("scrcpy_%08x", o.SCID) }

// Command is the device shell line that runs the server: video H.264, no audio, control on, reached over adb forward.
func (o Options) Command() string {
	args := []string{
		"CLASSPATH=" + DevicePath, "app_process", "/", "com.genymobile.scrcpy.Server", Version,
		fmt.Sprintf("scid=%08x", o.SCID), "log_level=info", "tunnel_forward=true", "audio=false", "control=true",
		"video_codec=h264", fmt.Sprintf("max_size=%d", o.MaxSize), "send_device_meta=false", "send_dummy_byte=true", "cleanup=true",
	}
	if o.BitRate > 0 {
		args = append(args, fmt.Sprintf("video_bit_rate=%d", o.BitRate))
	}

	return strings.Join(args, " ")
}
