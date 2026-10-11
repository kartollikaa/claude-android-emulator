// Package emulator talks to one running emulator over its EmulatorController gRPC service.
package emulator

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/discovery"
	pb "github.com/kartollikaa/claude-android-emulator/bridge/internal/emulatorpb"
)

// A full-size RGB888 frame exceeds gRPC's default 4 MiB receive limit.
const maxFrameBytes = 64 << 20

const dialTimeout = 5 * time.Second

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

type Conn struct {
	Emulator discovery.Emulator
	Device   Size
	conn     *grpc.ClientConn
	client   pb.EmulatorControllerClient
	logger   *slog.Logger
	// The rotation of the newest frame; touches arrive in its coordinates.
	rotation atomic.Int32
}

type bearerToken string

func (token bearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(token)}, nil
}

// The emulator serves gRPC in plaintext on localhost.
func (bearerToken) RequireTransportSecurity() bool { return false }

// Dial connects to the emulator and reads its native portrait panel size.
func Dial(ctx context.Context, emulator discovery.Emulator, logger *slog.Logger) (*Conn, error) {
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
	emulatorStatus, err := client.GetStatus(ctx, &emptypb.Empty{})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("emulator on gRPC port %d: %w", emulator.GRPCPort, err)
	}
	device := panelSize(emulatorStatus)
	if device.Width <= 0 || device.Height <= 0 {
		conn.Close()
		return nil, fmt.Errorf("emulator on gRPC port %d reported no panel size", emulator.GRPCPort)
	}

	return &Conn{Emulator: emulator, Device: device, conn: conn, client: client, logger: logger}, nil
}

func (c *Conn) Close() error { return c.conn.Close() }

// panelSize is the hardware config's hw.lcd size: the portrait panel whatever the rotation, known before the display is up.
func panelSize(emulatorStatus *pb.EmulatorStatus) Size {
	config := emulatorStatus.GetPlatformConfig()
	if len(config) == 0 {
		config = map[string]string{}
		for _, entry := range emulatorStatus.GetHardwareConfig().GetEntry() {
			config[entry.GetKey()] = entry.GetValue()
		}
	}
	width, _ := strconv.Atoi(config["hw.lcd.width"])
	height, _ := strconv.Atoi(config["hw.lcd.height"])

	return Size{Width: width, Height: height}
}

// StreamBox is the square that frames streamed with a short side of width fit in, upright or on their side.
func (c *Conn) StreamBox(width int) Size {
	short, long := min(c.Device.Width, c.Device.Height), max(c.Device.Width, c.Device.Height)
	side := int(math.Round(float64(min(width, short)) * float64(long) / float64(short)))

	return Size{Width: side, Height: side}
}

// Stream calls onFrame for each new screen image until ctx ends or the emulator goes away.
func (c *Conn) Stream(ctx context.Context, size Size, onFrame func(Frame)) error {
	frames, err := c.client.StreamScreenshot(ctx, &pb.ImageFormat{
		Format: pb.ImageFormat_RGB888,
		Width:  uint32(size.Width),
		Height: uint32(size.Height),
	})
	if err != nil {
		return err
	}
	isMismatchLogged := false
	for {
		image, err := frames.Recv()
		if err != nil {
			return err
		}
		width, height := int(image.GetFormat().GetWidth()), int(image.GetFormat().GetHeight())
		if len(image.GetImage()) != width*height*3 {
			if !isMismatchLogged {
				c.logger.Warn("dropping frames that are not RGB888", "width", width, "height", height, "bytes", len(image.GetImage()))
				isMismatchLogged = true
			}
			continue
		}
		rotation := Rotation(image.GetFormat().GetRotation().GetRotation())
		if width > 0 && height > 0 {
			c.rotation.Store(int32(rotation))
		}
		onFrame(Frame{Seq: image.GetSeq(), Width: width, Height: height, Rotation: rotation, RGB: image.GetImage()})
	}
}

// Touch presses (isDown) or releases one finger at a point given as fractions of the screen as the newest frame shows it.
func (c *Conn) Touch(ctx context.Context, x, y float64, isDown bool) error {
	pressure := int32(0)
	if isDown {
		pressure = 1
	}
	panelX, panelY := Rotation(c.rotation.Load()).toPanel(x, y)
	_, err := c.client.SendTouch(ctx, &pb.TouchEvent{Touches: []*pb.Touch{{
		X:        toPixel(panelX, c.Device.Width),
		Y:        toPixel(panelY, c.Device.Height),
		Pressure: pressure,
	}}})

	return err
}

// toPanel turns fractions of the upright picture into fractions of the natural panel, which the touchscreen reports in.
func (r Rotation) toPanel(x, y float64) (float64, float64) {
	switch r & 3 {
	case Landscape:
		return 1 - y, x
	case ReversePortrait:
		return 1 - x, 1 - y
	case ReverseLandscape:
		return y, 1 - x
	}

	return x, y
}

// Rotate turns the device quarters of a turn, counter-clockwise for a positive count, keeping its tilt.
func (c *Conn) Rotate(ctx context.Context, quarters int) error {
	current, err := c.client.GetPhysicalModel(ctx, &pb.PhysicalModelValue{Target: pb.PhysicalModelValue_ROTATION})
	if err != nil {
		return err
	}
	angles := append(current.GetValue().GetData(), 0, 0, 0)[:3]
	z := math.Round(float64(angles[2])/90)*90 + float64(90*quarters)
	angles[2] = float32(math.Mod(math.Mod(z+180, 360)+360, 360) - 180)
	_, err = c.client.SetPhysicalModel(ctx, &pb.PhysicalModelValue{
		Target:        pb.PhysicalModelValue_ROTATION,
		Value:         &pb.ParameterValue{Data: angles},
		Interpolation: pb.PhysicalModelValue_STEP,
	})

	return err
}

// Screenshot returns the screen as the emulator encodes it: a PNG at the device's full resolution.
func (c *Conn) Screenshot(ctx context.Context) ([]byte, error) {
	image, err := c.client.GetScreenshot(ctx, &pb.ImageFormat{Format: pb.ImageFormat_PNG})
	if err != nil {
		return nil, err
	}

	return image.GetImage(), nil
}

// Key presses and releases a named key (GoHome, GoBack, AppSwitch, Enter, Backspace, ...).
func (c *Conn) Key(ctx context.Context, key string) error {
	_, err := c.client.SendKey(ctx, &pb.KeyboardEvent{EventType: pb.KeyboardEvent_keypress, Key: key})

	return err
}

// Shutdown asks the emulator to power off gracefully, as closing its window does.
func (c *Conn) Shutdown(ctx context.Context) error {
	_, err := c.client.SetVmState(ctx, &pb.VmRunState{State: pb.VmRunState_SHUTDOWN})

	return err
}

// Booted reports whether the device finished booting; an emulator too old to say counts as booted.
func (c *Conn) Booted(ctx context.Context) (bool, error) {
	emulatorStatus, err := c.client.GetStatus(ctx, &emptypb.Empty{})
	if status.Code(err) == codes.Unimplemented {
		return true, nil
	}
	if err != nil {
		return false, err
	}

	return emulatorStatus.GetBooted(), nil
}

func (c *Conn) Text(ctx context.Context, text string) error {
	_, err := c.client.SendKey(ctx, &pb.KeyboardEvent{EventType: pb.KeyboardEvent_keypress, Text: text})

	return err
}

func toPixel(fraction float64, extent int) int32 {
	clamped := math.Min(1, math.Max(0, fraction))

	return int32(math.Round(clamped * float64(extent-1)))
}
