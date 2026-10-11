// Package emulator talks to one running emulator over its EmulatorController gRPC service.
package emulator

import (
	"context"
	"fmt"
	"math"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/discovery"
	pb "github.com/kartollikaa/claude-android-emulator/bridge/internal/emulatorpb"
)

// A full-size RGB888 frame exceeds gRPC's default 4 MiB receive limit.
const maxFrameBytes = 64 << 20

const dialTimeout = 5 * time.Second

// Size is a width and height in pixels.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Frame is one screen image: RGB888, rows top-down.
type Frame struct {
	Seq    uint32
	Width  int
	Height int
	RGB    []byte
}

// Conn is an open connection to one emulator.
type Conn struct {
	Emulator discovery.Emulator
	Device   Size
	conn     *grpc.ClientConn
	client   pb.EmulatorControllerClient
}

type bearerToken string

func (token bearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(token)}, nil
}

// The emulator serves gRPC in plaintext on localhost.
func (bearerToken) RequireTransportSecurity() bool { return false }

// Dial connects to the emulator and reads its native screen size.
func Dial(ctx context.Context, emulator discovery.Emulator) (*Conn, error) {
	options := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxFrameBytes)),
	}
	if emulator.Token != "" {
		options = append(options, grpc.WithPerRPCCredentials(bearerToken(emulator.Token)))
	}
	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", emulator.GRPCPort), options...)
	if err != nil {
		return nil, err
	}
	client := pb.NewEmulatorControllerClient(conn)

	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	shot, err := client.GetScreenshot(ctx, &pb.ImageFormat{Format: pb.ImageFormat_PNG})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("emulator on gRPC port %d: %w", emulator.GRPCPort, err)
	}
	device := Size{Width: int(shot.GetFormat().GetWidth()), Height: int(shot.GetFormat().GetHeight())}
	if device.Width <= 0 || device.Height <= 0 {
		conn.Close()
		return nil, fmt.Errorf("emulator on gRPC port %d reported a %dx%d screen", emulator.GRPCPort, device.Width, device.Height)
	}

	return &Conn{Emulator: emulator, Device: device, conn: conn, client: client}, nil
}

// Close drops the connection.
func (c *Conn) Close() error { return c.conn.Close() }

// StreamSize is the size frames arrive at when streamed at width, keeping the screen's aspect.
func (c *Conn) StreamSize(width int) Size {
	width = min(width, c.Device.Width)

	return Size{Width: width, Height: int(math.Round(float64(width) * float64(c.Device.Height) / float64(c.Device.Width)))}
}

// Stream calls onFrame for each new screen image until ctx ends or the emulator goes away.
// An unchanged screen sends nothing.
func (c *Conn) Stream(ctx context.Context, size Size, onFrame func(Frame)) error {
	frames, err := c.client.StreamScreenshot(ctx, &pb.ImageFormat{
		Format: pb.ImageFormat_RGB888,
		Width:  uint32(size.Width),
		Height: uint32(size.Height),
	})
	if err != nil {
		return err
	}
	for {
		image, err := frames.Recv()
		if err != nil {
			return err
		}
		width, height := int(image.GetFormat().GetWidth()), int(image.GetFormat().GetHeight())
		if len(image.GetImage()) != width*height*3 {
			continue
		}
		onFrame(Frame{Seq: image.GetSeq(), Width: width, Height: height, RGB: image.GetImage()})
	}
}

// Touch presses (isDown) or releases one finger at a point given as fractions of the screen.
func (c *Conn) Touch(ctx context.Context, x, y float64, isDown bool) error {
	pressure := int32(0)
	if isDown {
		pressure = 1
	}
	_, err := c.client.SendTouch(ctx, &pb.TouchEvent{Touches: []*pb.Touch{{
		X:        toPixel(x, c.Device.Width),
		Y:        toPixel(y, c.Device.Height),
		Pressure: pressure,
	}}})

	return err
}

// Key presses and releases a named key (GoHome, GoBack, AppSwitch, Enter, Backspace, ...).
func (c *Conn) Key(ctx context.Context, key string) error {
	_, err := c.client.SendKey(ctx, &pb.KeyboardEvent{EventType: pb.KeyboardEvent_keypress, Key: key})

	return err
}

// Text types text as the keyboard would.
func (c *Conn) Text(ctx context.Context, text string) error {
	_, err := c.client.SendKey(ctx, &pb.KeyboardEvent{EventType: pb.KeyboardEvent_keypress, Text: text})

	return err
}

func toPixel(fraction float64, extent int) int32 {
	clamped := math.Min(1, math.Max(0, fraction))

	return int32(math.Round(clamped * float64(extent-1)))
}
