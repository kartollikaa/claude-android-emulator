// Package fakeemulator serves the EmulatorController gRPC service in process, for tests.
package fakeemulator

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/kartollikaa/claude-android-emulator/bridge/internal/emulatorpb"
)

type Fake struct {
	pb.UnimplementedEmulatorControllerServer

	Width, Height int
	Port          int
	Token         string
	// AVD, when set before WriteINI, is the name the discovery file gives as avd.id.
	AVD string
	// Rotated makes screenshots landscape, as an emulator turned on its side answers.
	Rotated bool
	// PNG is what getScreenshot answers in the PNG format.
	PNG []byte

	server  *grpc.Server
	streams chan chan *pb.Image

	mu       sync.Mutex
	isBooted bool
	angles   []float32
	turns    int
	readTime time.Duration
	asked    []*pb.ImageFormat
	vmStates []pb.VmRunState_RunState
	touches  []*pb.Touch
	keys     []*pb.KeyboardEvent
	calls    int
	rejected int
}

// Start serves a fake emulator with a native screen of width x height; token, when set, is required.
func Start(t *testing.T, width, height int, token string) *Fake {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &Fake{
		Width:    width,
		Height:   height,
		Port:     listener.Addr().(*net.TCPAddr).Port,
		Token:    token,
		streams:  make(chan chan *pb.Image, 4),
		isBooted: true,
		angles:   []float32{0, 0, 0},
	}
	fake.server = grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			if err := fake.authorize(ctx); err != nil {
				return nil, err
			}
			return handler(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if err := fake.authorize(stream.Context()); err != nil {
				return err
			}
			return handler(srv, stream)
		}),
	)
	pb.RegisterEmulatorControllerServer(fake.server, fake)
	go fake.server.Serve(listener)
	t.Cleanup(fake.Stop)

	return fake
}

func (f *Fake) authorize(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.Token == "" {
		return nil
	}
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) == 1 && values[0] == "Bearer "+f.Token {
		return nil
	}
	f.rejected++

	return status.Error(codes.Unauthenticated, "missing or wrong token")
}

// WriteINI writes the discovery file the real emulator writes on start.
func (f *Fake) WriteINI(t *testing.T, dir string, pid int) string {
	t.Helper()
	text := fmt.Sprintf("port.serial=%d\ngrpc.port=%d\n", 5554+2*pid, f.Port)
	if f.AVD != "" {
		text += "avd.id=" + f.AVD + "\n"
	}
	if f.Token != "" {
		text += "grpc.token=" + f.Token + "\n"
	}
	path := filepath.Join(dir, fmt.Sprintf("pid_%d.ini", pid))
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// Stop ends every call and closes the port, as a killed emulator does.
func (f *Fake) Stop() { f.server.Stop() }

// NextStream waits for the bridge to open a screenshot stream and returns a sender of frames into it.
func (f *Fake) NextStream(t *testing.T, ctx context.Context) chan<- *pb.Image {
	t.Helper()
	select {
	case stream := <-f.streams:
		return stream
	case <-ctx.Done():
		t.Fatal("the bridge never opened a screenshot stream")
		return nil
	}
}

// Turned marks frame as drawn for a device turned to rotation.
func Turned(frame *pb.Image, rotation pb.Rotation_SkinRotation) *pb.Image {
	frame.Format.Rotation = &pb.Rotation{Rotation: rotation}
	return frame
}

// Frame builds an RGB888 frame of a repeating byte pattern that starts at fill.
func Frame(seq uint32, width, height int, fill byte) *pb.Image {
	pixels := make([]byte, width*height*3)
	for i := range pixels {
		pixels[i] = fill + byte(i%7)
	}

	return &pb.Image{
		Format: &pb.ImageFormat{Format: pb.ImageFormat_RGB888, Width: uint32(width), Height: uint32(height)},
		Image:  pixels,
		Seq:    seq,
	}
}

func (f *Fake) Touches() []*pb.Touch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.Touch(nil), f.touches...)
}

func (f *Fake) Keys() []*pb.KeyboardEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.KeyboardEvent(nil), f.keys...)
}

// Calls and Rejected count the calls received and the ones refused for a missing token.
func (f *Fake) Calls() (calls, rejected int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.rejected
}

// SetBooted sets what getStatus answers; a fake starts booted.
func (f *Fake) SetBooted(isBooted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.isBooted = isBooted
}

func (f *Fake) GetStatus(context.Context, *emptypb.Empty) (*pb.EmulatorStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &pb.EmulatorStatus{
		Booted: f.isBooted,
		HardwareConfig: &pb.EntryList{Entry: []*pb.Entry{
			{Key: "hw.lcd.width", Value: fmt.Sprint(f.Width)},
			{Key: "hw.lcd.height", Value: fmt.Sprint(f.Height)},
		}},
	}, nil
}

func (f *Fake) GetScreenshot(context.Context, *pb.ImageFormat) (*pb.Image, error) {
	width, height := f.Width, f.Height
	if f.Rotated {
		width, height = height, width
	}
	return &pb.Image{Format: &pb.ImageFormat{Format: pb.ImageFormat_PNG, Width: uint32(width), Height: uint32(height)}, Image: f.PNG}, nil
}

// SetAngles sets the device's rotation sensor, [x, y, z] in degrees.
func (f *Fake) SetAngles(angles ...float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.angles = angles
}

// SetSensorReadTime makes getPhysicalModel take that long, so calls made together overlap.
func (f *Fake) SetSensorReadTime(readTime time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readTime = readTime
}

// Angles returns the rotation sensor and how many times it was set.
func (f *Fake) Angles() (angles []float32, turns int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]float32(nil), f.angles...), f.turns
}

func (f *Fake) GetPhysicalModel(_ context.Context, value *pb.PhysicalModelValue) (*pb.PhysicalModelValue, error) {
	f.mu.Lock()
	readTime := f.readTime
	f.mu.Unlock()
	time.Sleep(readTime)
	f.mu.Lock()
	defer f.mu.Unlock()
	if value.GetTarget() != pb.PhysicalModelValue_ROTATION {
		return nil, status.Error(codes.Unimplemented, "the fake models rotation only")
	}
	return &pb.PhysicalModelValue{Target: value.GetTarget(), Value: &pb.ParameterValue{Data: append([]float32(nil), f.angles...)}}, nil
}

func (f *Fake) SetPhysicalModel(_ context.Context, value *pb.PhysicalModelValue) (*emptypb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if value.GetTarget() != pb.PhysicalModelValue_ROTATION {
		return nil, status.Error(codes.Unimplemented, "the fake models rotation only")
	}
	f.angles = append([]float32(nil), value.GetValue().GetData()...)
	f.turns++
	return &emptypb.Empty{}, nil
}

// Asked returns the image formats the screenshot streams were opened with.
func (f *Fake) Asked() []*pb.ImageFormat {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.ImageFormat(nil), f.asked...)
}

// VmStates returns the VM states asked for so far.
func (f *Fake) VmStates() []pb.VmRunState_RunState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pb.VmRunState_RunState(nil), f.vmStates...)
}

// SetVmState records the state; SHUTDOWN ends the fake as the real emulator powers off.
func (f *Fake) SetVmState(_ context.Context, state *pb.VmRunState) (*emptypb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vmStates = append(f.vmStates, state.GetState())
	if state.GetState() == pb.VmRunState_SHUTDOWN {
		go f.Stop()
	}
	return &emptypb.Empty{}, nil
}

func (f *Fake) StreamScreenshot(format *pb.ImageFormat, stream grpc.ServerStreamingServer[pb.Image]) error {
	f.mu.Lock()
	f.asked = append(f.asked, format)
	f.mu.Unlock()
	// Buffered, so a test sending into a stream that already ended fails on its read instead of hanging.
	frames := make(chan *pb.Image, 16)
	select {
	case f.streams <- frames:
	case <-stream.Context().Done():
		return nil
	}
	for {
		select {
		case frame := <-frames:
			if err := stream.Send(frame); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

func (f *Fake) SendTouch(_ context.Context, event *pb.TouchEvent) (*emptypb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touches = append(f.touches, event.GetTouches()...)
	return &emptypb.Empty{}, nil
}

func (f *Fake) SendKey(_ context.Context, event *pb.KeyboardEvent) (*emptypb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, event)
	return &emptypb.Empty{}, nil
}
