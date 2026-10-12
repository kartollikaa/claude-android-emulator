// Package emulator talks to one running emulator over its EmulatorController gRPC service.
package emulator

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/device"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/discovery"
	pb "github.com/kartollikaa/claude-android-emulator/bridge/internal/emulatorpb"
)

// A full-size RGB888 frame exceeds gRPC's default 4 MiB receive limit.
const maxFrameBytes = 64 << 20

const dialTimeout = 5 * time.Second

var _ device.Device = (*Conn)(nil)

// Conn is a device.Device for one running emulator.
type Conn struct {
	discovered discovery.Emulator
	panel      device.Size
	conn       *grpc.ClientConn
	client     pb.EmulatorControllerClient
	logger     *slog.Logger
	// The rotation of the newest frame; touches arrive in its coordinates.
	rotation atomic.Int32
	// Rotate reads the sensor before setting it, so two at once would turn the device once.
	rotating sync.Mutex
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
	panel := panelSize(emulatorStatus)
	if panel.Width <= 0 || panel.Height <= 0 {
		conn.Close()
		return nil, fmt.Errorf("emulator on gRPC port %d reported no panel size", emulator.GRPCPort)
	}

	return &Conn{discovered: emulator, panel: panel, conn: conn, client: client, logger: logger}, nil
}

func (c *Conn) Serial() string { return c.discovered.Serial }

func (c *Conn) Name() string { return c.discovered.AVD }

func (c *Conn) Panel() device.Size { return c.panel }

func (c *Conn) Close() error { return c.conn.Close() }

// panelSize is the hardware config's hw.lcd size: the portrait panel whatever the rotation, known before the display is up.
func panelSize(emulatorStatus *pb.EmulatorStatus) device.Size {
	config := emulatorStatus.GetPlatformConfig()
	if len(config) == 0 {
		config = map[string]string{}
		for _, entry := range emulatorStatus.GetHardwareConfig().GetEntry() {
			config[entry.GetKey()] = entry.GetValue()
		}
	}
	width, _ := strconv.Atoi(config["hw.lcd.width"])
	height, _ := strconv.Atoi(config["hw.lcd.height"])

	return device.Size{Width: width, Height: height}
}

// streamBox is the square that frames streamed with a short side of width fit in, upright or on their side.
func (c *Conn) streamBox(width int) device.Size {
	short, long := min(c.panel.Width, c.panel.Height), max(c.panel.Width, c.panel.Height)
	side := int(math.Round(float64(min(width, short)) * float64(long) / float64(short)))

	return device.Size{Width: side, Height: side}
}

func (c *Conn) Stream(ctx context.Context, streamWidth int, onFrame func(device.Frame)) error {
	size := c.streamBox(streamWidth)
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
		rotation := device.Rotation(image.GetFormat().GetRotation().GetRotation())
		if width > 0 && height > 0 {
			c.rotation.Store(int32(rotation))
		}
		onFrame(device.Frame{Seq: image.GetSeq(), Width: width, Height: height, Rotation: rotation, RGB: image.GetImage()})
	}
}

// Touch goes to the panel pixel under the point as the newest frame shows it.
func (c *Conn) Touch(ctx context.Context, x, y float64, isDown bool) error {
	pressure := int32(0)
	if isDown {
		pressure = 1
	}
	panelX, panelY := toPanel(device.Rotation(c.rotation.Load()), x, y)
	_, err := c.client.SendTouch(ctx, &pb.TouchEvent{Touches: []*pb.Touch{{
		X:        toPixel(panelX, c.panel.Width),
		Y:        toPixel(panelY, c.panel.Height),
		Pressure: pressure,
	}}})

	return err
}

// toPanel turns fractions of the upright picture into fractions of the natural panel, which the touchscreen reports in.
func toPanel(r device.Rotation, x, y float64) (float64, float64) {
	switch r & 3 {
	case device.Landscape:
		return 1 - y, x
	case device.ReversePortrait:
		return 1 - x, 1 - y
	case device.ReverseLandscape:
		return y, 1 - x
	}

	return x, y
}

// Rotate turns the virtual sensor, keeping its tilt.
func (c *Conn) Rotate(ctx context.Context, quarters int) error {
	c.rotating.Lock()
	defer c.rotating.Unlock()
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

// Screenshot returns the PNG the emulator encodes.
func (c *Conn) Screenshot(ctx context.Context) ([]byte, error) {
	image, err := c.client.GetScreenshot(ctx, &pb.ImageFormat{Format: pb.ImageFormat_PNG})
	if err != nil {
		return nil, err
	}

	return image.GetImage(), nil
}

// ScreenshotAt has the emulator scale the screen.
func (c *Conn) ScreenshotAt(ctx context.Context, size device.Size) (device.Frame, error) {
	image, err := c.client.GetScreenshot(ctx, &pb.ImageFormat{Format: pb.ImageFormat_RGB888, Width: uint32(size.Width), Height: uint32(size.Height)})
	if err != nil {
		return device.Frame{}, err
	}
	width, height := int(image.GetFormat().GetWidth()), int(image.GetFormat().GetHeight())
	if width <= 0 || height <= 0 || len(image.GetImage()) != width*height*3 {
		return device.Frame{}, fmt.Errorf("the emulator answered a %d×%d screenshot of %d bytes, not RGB888", width, height, len(image.GetImage()))
	}

	return device.Frame{Seq: image.GetSeq(), Width: width, Height: height, Rotation: c.Rotation(), RGB: image.GetImage()}, nil
}

func (c *Conn) Upright() device.Size {
	if c.Rotation()&1 == 1 {
		return device.Size{Width: c.panel.Height, Height: c.panel.Width}
	}

	return c.panel
}

// Rotation is the device's rotation as the newest frame shows it.
func (c *Conn) Rotation() device.Rotation { return device.Rotation(c.rotation.Load()) }

func (c *Conn) Key(ctx context.Context, key string) error {
	_, err := c.client.SendKey(ctx, &pb.KeyboardEvent{EventType: pb.KeyboardEvent_keypress, Key: key})

	return err
}

// Shutdown asks the emulator to power off gracefully, as closing its window does.
func (c *Conn) Shutdown(ctx context.Context) error {
	_, err := c.client.SetVmState(ctx, &pb.VmRunState{State: pb.VmRunState_SHUTDOWN})

	return err
}

// Booted counts an emulator too old to say as booted.
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
