//go:build unix

package live

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/device"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/fakeemulator"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/scrcpy"
)

const (
	phoneSerial = "5C111JEBF10513"
	// A Pixel 9a's config packet: SPS and PPS of a 536×1208 video.
	phoneConfig = "0000000167640020acb404404cf2cb35010101078a15500000000168ee0d8b"
	phoneUSB    = phoneSerial + " device usb:0-1 product:tegu model:Pixel_9a device:tegu transport_id:18\n"
)

var videoSize = device.Size{Width: 536, Height: 1208}

// A phone attaches through a dozen adb calls, each a shell script here, which takes a while on a loaded machine.
const attachPatience = 10 * time.Second

// The fake phone's getprop, wm and settings: a Pixel 9a whose settings live in setting-<name> files.
var phoneCommands = map[string]string{
	"getprop": `#!/bin/sh
d="$(dirname "$0")/.."
case "$1" in
ro.product.model) echo "Pixel 9a" ;;
sys.boot_completed) echo 1 ;;
ro.serialno) cat "$d/serialno-$ADB_SERIAL" 2>/dev/null || echo "$ADB_SERIAL" ;;
esac
`,
	"wm": `#!/bin/sh
echo "Physical size: 8x16"
`,
	"settings": `#!/bin/sh
d="$(dirname "$0")/.."
case "$1" in
get) cat "$d/setting-$3" 2>/dev/null || echo null ;;
put) echo "$3=$4" >> "$d/settings-put"; echo "$4" > "$d/setting-$3" ;;
esac
`,
}

// fakeScrcpy stands in for the scrcpy server behind adb forward: each video connection gets the dummy byte and
// the metadata, and each control connection's messages are kept.
type fakeScrcpy struct {
	listener net.Listener
	videos   chan net.Conn
	mu       sync.Mutex
	control  []byte
}

func startScrcpy(t *testing.T, tools string) *fakeScrcpy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	writeFile(t, filepath.Join(tools, "scrcpy-port"), strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)+"\n")
	server := &fakeScrcpy{listener: listener, videos: make(chan net.Conn, 4)}
	go server.serve()
	return server
}

func (s *fakeScrcpy) serve() {
	for {
		video, err := s.listener.Accept()
		if err != nil {
			return
		}
		meta := []byte{0x68, 0x32, 0x36, 0x34, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(meta[4:], uint32(videoSize.Width))
		binary.BigEndian.PutUint32(meta[8:], uint32(videoSize.Height))
		video.Write(append([]byte{0}, meta...))
		control, err := s.listener.Accept()
		if err != nil {
			video.Close()
			return
		}
		go func() {
			buffer := make([]byte, 1024)
			for {
				n, err := control.Read(buffer)
				s.mu.Lock()
				s.control = append(s.control, buffer[:n]...)
				s.mu.Unlock()
				if err != nil {
					return
				}
			}
		}()
		s.videos <- video
	}
}

func (s *fakeScrcpy) nextVideo(t *testing.T) net.Conn {
	t.Helper()
	select {
	case video := <-s.videos:
		t.Cleanup(func() { video.Close() })
		return video
	case <-time.After(attachPatience):
		t.Fatal("the bridge never connected to the scrcpy server")
		return nil
	}
}

func (s *fakeScrcpy) received() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.control)
}

// awaitControl waits until the control socket has received want, after what it had received before.
func (s *fakeScrcpy) awaitControl(t *testing.T, before int, want []byte) {
	t.Helper()
	eventually(t, func() bool { return bytes.Equal(s.received()[before:], want) }, "control message "+hex.EncodeToString(want))
}

func sendPacket(t *testing.T, video net.Conn, isConfig, isKeyFrame bool, data []byte) {
	t.Helper()
	header := make([]byte, 12)
	flags := uint64(0)
	if isConfig {
		flags |= 1 << 63
	}
	if isKeyFrame {
		flags |= 1 << 62
	}
	binary.BigEndian.PutUint64(header, flags)
	binary.BigEndian.PutUint32(header[8:], uint32(len(data)))
	if _, err := video.Write(append(header, data...)); err != nil {
		t.Fatal(err)
	}
}

func configPacket() []byte {
	data, _ := hex.DecodeString(phoneConfig)
	return data
}

type phoneHarness struct {
	*harness
	tools  string
	scrcpy *fakeScrcpy
}

// phoneBridge starts a bridge whose adb lists the given devices, with no emulator running.
func phoneBridge(t *testing.T, devices string, options ...func(*Config)) *phoneHarness {
	t.Helper()
	root, tools := sdkWithADB(t)
	if err := os.MkdirAll(filepath.Join(tools, "device-bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, script := range phoneCommands {
		if err := os.WriteFile(filepath.Join(tools, "device-bin", name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(tools, "devices"), "List of devices attached\n"+devices+"\n")
	writeFile(t, filepath.Join(tools, "window"), "  DisplayRotation\n    mRotation=0 mDeferredRotationPauseCount=0\n")
	var screen bytes.Buffer
	picture := image.NewNRGBA(image.Rect(0, 0, deviceWidth, deviceHeight))
	for i := range picture.Pix {
		picture.Pix[i] = 0xff
	}
	png.Encode(&screen, picture)
	writeFile(t, filepath.Join(tools, "screen.png"), screen.String())
	server := startScrcpy(t, tools)
	h := startBridge(t, append([]func(*Config){withSDK(root), atMoment}, options...)...)

	return &phoneHarness{harness: h, tools: tools, scrcpy: server}
}

// streaming waits for the bridge to start the server, then sends a config packet and a key frame.
func (p *phoneHarness) streaming(t *testing.T) net.Conn {
	t.Helper()
	video := p.scrcpy.nextVideo(t)
	sendPacket(t, video, true, false, configPacket())
	sendPacket(t, video, false, true, []byte("key frame"))
	p.waitForWithin(t, attachPatience, func(state State) bool { return state.Stream == videoSize && state.Booted })
	return video
}

type livePacket struct {
	width, height uint32
	isConfig      bool
	isKeyFrame    bool
	data          string
}

func readPacket(t *testing.T, ctx context.Context, socket *websocket.Conn) livePacket {
	t.Helper()
	for {
		kind, data, err := socket.Read(ctx)
		if err != nil {
			t.Fatalf("no packet arrived: %v", err)
		}
		if kind != websocket.MessageBinary {
			continue
		}
		if len(data) < frameHeaderBytes || data[0] != frameH264 {
			t.Fatalf("a binary message of %d bytes is not an H.264 packet", len(data))
		}
		flags := binary.LittleEndian.Uint32(data[9:])
		return livePacket{
			width: binary.LittleEndian.Uint32(data[1:]), height: binary.LittleEndian.Uint32(data[5:]),
			isConfig: flags&packetIsConfig != 0, isKeyFrame: flags&packetIsKeyFrame != 0, data: string(data[frameHeaderBytes:]),
		}
	}
}

func TestPhoneShowsWhenNoEmulatorRuns(t *testing.T) {
	p := phoneBridge(t, phoneUSB)
	p.streaming(t)

	state := p.status(t)
	if !state.Connected || !state.Booted || state.Kind != device.Phone || state.Serial != phoneSerial || state.Model != "Pixel 9a" ||
		state.AVD != "" || state.WiFi || state.Device != deviceSize() || state.Rotation != device.Portrait {
		t.Fatalf("status %+v, want the booted Pixel 9a over USB, its 8×16 panel upright", state)
	}
	calls := strings.Join(adbCalls(p.tools), "\n")
	scid := regexp.MustCompile(`forward tcp:0 localabstract:scrcpy_([0-9a-f]{8})`).FindStringSubmatch(calls)
	if scid == nil {
		t.Fatalf("no forward to a scrcpy socket in the calls:\n%s", calls)
	}
	for _, want := range []string{
		"-s " + phoneSerial + " push ",
		" " + scrcpy.DevicePath + "\n",
		"shell CLASSPATH=" + scrcpy.DevicePath + " app_process / com.genymobile.scrcpy.Server " + scrcpy.Version + " scid=" + scid[1] + " ",
		" max_size=8 ",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("the calls lack %q:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "video_bit_rate") {
		t.Errorf("a phone over USB got a lowered bit rate:\n%s", calls)
	}
}

func TestPhoneOverWiFiStreamsAtALowerBitRate(t *testing.T) {
	p := phoneBridge(t, "192.168.1.20:5555 device product:tegu model:Pixel_9a transport_id:19")
	p.streaming(t)

	if state := p.status(t); !state.WiFi || state.Serial != "192.168.1.20:5555" {
		t.Errorf("status %+v, want the phone over Wi-Fi", state)
	}
	if calls := strings.Join(adbCalls(p.tools), "\n"); !strings.Contains(calls, " video_bit_rate=4000000") {
		t.Errorf("the server over Wi-Fi was not given a lower bit rate:\n%s", calls)
	}
	status, answer := control(t, p.harness, "/screenshot", `{}`)
	if name := filepath.Base(savedPath(t, answer)); status != http.StatusOK || name != "Screenshot_192.168.1.20-5555_20261011-170005.png" {
		t.Errorf("screenshot answered %d and saved %s, want the serial made safe for a file name", status, name)
	}
}

func TestPhoneListedTwiceShowsOverUSB(t *testing.T) {
	p := phoneBridge(t, "192.168.1.20:5555 device product:tegu transport_id:19\n"+phoneUSB)
	writeFile(t, filepath.Join(p.tools, "serialno-192.168.1.20:5555"), phoneSerial)
	p.streaming(t)

	if state := p.status(t); state.Serial != phoneSerial || state.WiFi {
		t.Errorf("status %+v, want the phone over USB", state)
	}
}

func TestUnauthorizedPhoneShowsInTheStatus(t *testing.T) {
	p := phoneBridge(t, "R5CT30ABCDE unauthorized usb:1-2 transport_id:21")

	state := p.waitForWithin(t, attachPatience, func(state State) bool { return state.Unauthorized != "" })
	if state.Connected || state.Unauthorized != "R5CT30ABCDE" {
		t.Errorf("status %+v, want nothing connected and R5CT30ABCDE waiting for USB debugging", state)
	}
}

func TestLateViewerGetsTheConfigAndTheBacklog(t *testing.T) {
	p := phoneBridge(t, phoneUSB)
	video := p.streaming(t)
	ctx := testContext(t)
	first := p.dialLive(t, ctx)
	want := []livePacket{
		{536, 1208, true, false, string(configPacket())},
		{536, 1208, false, true, "key frame"},
		{536, 1208, false, false, "delta 1"},
	}
	sendPacket(t, video, false, false, []byte("delta 1"))
	for _, packet := range want {
		if got := readPacket(t, ctx, first); got != packet {
			t.Fatalf("first viewer got %+v, want %+v", got, packet)
		}
	}

	late := p.dialLive(t, ctx)
	for _, packet := range want {
		if got := readPacket(t, ctx, late); got != packet {
			t.Fatalf("late viewer got %+v, want %+v", got, packet)
		}
	}
	sendPacket(t, video, false, false, []byte("delta 2"))
	for _, socket := range []*websocket.Conn{first, late} {
		if got := readPacket(t, ctx, socket); got.data != "delta 2" {
			t.Errorf("a viewer got %+v next, want delta 2", got)
		}
	}
}

func TestAViewerThatCannotStartAsksForAReset(t *testing.T) {
	p := phoneBridge(t, phoneUSB)
	video := p.streaming(t)
	for range 5 {
		sendPacket(t, video, false, false, make([]byte, maxBacklogBytes/4))
	}
	sendPacket(t, video, false, false, []byte("last"))
	eventually(t, func() bool {
		p.bridge.mu.Lock()
		defer p.bridge.mu.Unlock()
		return !p.bridge.video.isStartable
	}, "the backlog overflowing")
	before := len(p.scrcpy.received())

	ctx := testContext(t)
	socket := p.dialLive(t, ctx)
	p.scrcpy.awaitControl(t, before, scrcpy.ResetVideoMessage())
	sendPacket(t, video, true, false, configPacket())
	sendPacket(t, video, false, true, []byte("fresh key frame"))
	if got := readPacket(t, ctx, socket); !got.isConfig {
		t.Fatalf("the viewer started with %+v, want the config packet", got)
	}
	if got := readPacket(t, ctx, socket); got.data != "fresh key frame" {
		t.Fatalf("then %+v, want the fresh key frame", got)
	}
}

func TestPhoneInputGoesToTheControlSocket(t *testing.T) {
	p := phoneBridge(t, phoneUSB)
	p.streaming(t)

	before := len(p.scrcpy.received())
	if code := postInput(t, p.harness, "application/json", `{"type":"touch","phase":"down","x":0.5,"y":0.25}`); code != http.StatusNoContent {
		t.Fatalf("touch answered %d", code)
	}
	postInput(t, p.harness, "application/json", `{"type":"touch","phase":"move","x":0.5,"y":0.5}`)
	postInput(t, p.harness, "application/json", `{"type":"touch","phase":"up","x":0.5,"y":0.5}`)
	finger := func(action byte, x, y int32) []byte {
		return scrcpy.Touch{Action: action, Pointer: scrcpy.PointerFinger, X: x, Y: y, Width: 536, Height: 1208}.Message()
	}
	p.scrcpy.awaitControl(t, before, slices.Concat(finger(scrcpy.ActionDown, 268, 302), finger(scrcpy.ActionMove, 268, 604), finger(scrcpy.ActionUp, 268, 604)))

	before = len(p.scrcpy.received())
	postInput(t, p.harness, "application/json", `{"type":"key","key":"GoHome"}`)
	postInput(t, p.harness, "application/json", `{"type":"text","text":"hi"}`)
	p.scrcpy.awaitControl(t, before, slices.Concat(scrcpy.KeyMessage(scrcpy.ActionDown, 3), scrcpy.KeyMessage(scrcpy.ActionUp, 3), scrcpy.TextMessages("hi")[0]))

	if code := postInput(t, p.harness, "application/json", `{"type":"key","key":"Hyper"}`); code != http.StatusBadRequest {
		t.Errorf("an unknown key answered %d, want 400", code)
	}
}

func TestPhoneRotationSetsTheLockedRotation(t *testing.T) {
	p := phoneBridge(t, phoneUSB)
	p.streaming(t)
	writeFile(t, filepath.Join(p.tools, "setting-accelerometer_rotation"), "1\n")
	writeFile(t, filepath.Join(p.tools, "window"), "    mRotation=1 mDeferredRotationPauseCount=0\n")

	for _, step := range []struct {
		direction string
		want      string
	}{
		{"left", "accelerometer_rotation=0\nuser_rotation=2\n"},
		{"left", "accelerometer_rotation=0\nuser_rotation=3\n"},
		{"right", "accelerometer_rotation=0\nuser_rotation=2\n"},
	} {
		os.Remove(filepath.Join(p.tools, "settings-put"))
		if status, body := control(t, p.harness, "/rotate", `{"direction":"`+step.direction+`"}`); status != http.StatusNoContent {
			t.Fatalf("rotate %s answered %d %s", step.direction, status, body)
		}
		if put, _ := os.ReadFile(filepath.Join(p.tools, "settings-put")); string(put) != step.want {
			t.Errorf("rotate %s put %q, want %q", step.direction, put, step.want)
		}
	}
}

func TestPhoneRotationFollowsTheVideo(t *testing.T) {
	p := phoneBridge(t, phoneUSB)
	video := p.streaming(t)

	writeFile(t, filepath.Join(p.tools, "window"), "    mRotation=3 mDeferredRotationPauseCount=0\n")
	// A Baseline SPS of a 1208×536 video, then a PPS.
	turned, _ := hex.DecodeString("000000016742001fda0130117965400000000168ce3c80")
	sendPacket(t, video, true, false, turned)
	state := p.waitFor(t, func(state State) bool { return state.Stream.Width > state.Stream.Height })
	if state.Rotation != device.ReverseLandscape {
		t.Errorf("after the video turned: rotation %v, stream %+v, want reverse-landscape", state.Rotation, state.Stream)
	}

	before := len(p.scrcpy.received())
	postInput(t, p.harness, "application/json", `{"type":"touch","phase":"down","x":0,"y":1}`)
	turnedFinger := scrcpy.Touch{Action: scrcpy.ActionDown, Pointer: scrcpy.PointerFinger, X: 0, Y: 535, Width: 1208, Height: 536}
	p.scrcpy.awaitControl(t, before, turnedFinger.Message())
}

func TestPhoneScreenForClaude(t *testing.T) {
	p := phoneBridge(t, phoneUSB)
	p.streaming(t)

	status, answer := send(t, p.harness, http.MethodGet, "/screen", "", "", "")
	var screen Screen
	if err := json.Unmarshal([]byte(answer), &screen); status != http.StatusOK || err != nil {
		t.Fatalf("/screen answered %d %s", status, answer)
	}
	data, _ := base64.StdEncoding.DecodeString(screen.JPEG)
	picture, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil || picture.Bounds().Dx() != deviceWidth || picture.Bounds().Dy() != deviceHeight {
		t.Fatalf("the screen is not an %d×%d JPEG: %v", deviceWidth, deviceHeight, err)
	}

	status, answer = control(t, p.harness, "/screenshot", `{}`)
	saved, _ := os.ReadFile(savedPath(t, answer))
	if want, _ := os.ReadFile(filepath.Join(p.tools, "screen.png")); status != http.StatusOK || !bytes.Equal(saved, want) {
		t.Errorf("screenshot answered %d and saved %d bytes, want screencap's PNG", status, len(saved))
	}
}

func TestPhoneRefusesToShutDown(t *testing.T) {
	p := phoneBridge(t, phoneUSB)
	p.streaming(t)

	if status, body := send(t, p.harness, http.MethodPost, "/shutdown", "application/json", "", `{}`); status != http.StatusConflict {
		t.Errorf("shutdown of a phone answered %d %q, want 409", status, body)
	}
}

func TestStartWhileAPhoneShowsThenTheEmulatorTakesOver(t *testing.T) {
	p := phoneBridge(t, phoneUSB+"emulator-5556 device product:sdk_gphone64_arm64 transport_id:3\n")
	writeFile(t, filepath.Join(filepath.Dir(p.tools), "emulator", "emulator"), "#!/bin/sh\n"+stayingUp)
	os.Chmod(filepath.Join(filepath.Dir(p.tools), "emulator", "emulator"), 0o755)
	p.streaming(t)

	if status := start(t, p.harness, "Pixel_7"); status != http.StatusAccepted {
		t.Fatalf("start with a phone shown answered %d, want 202", status)
	}
	p.waitFor(t, func(state State) bool { return state.Launch.Phase == launchStarting && state.Kind == device.Phone })

	fake := fakeemulator.Start(t, deviceWidth, deviceHeight, "")
	fake.WriteINI(t, p.dir, 1)
	fake.NextStream(t, testContext(t))
	state := p.waitFor(t, func(state State) bool { return state.Kind == device.Emulator })
	if state.Serial != "emulator-5556" || state.Model != "" {
		t.Errorf("status %+v, want the emulator in the phone's place", state)
	}
	eventually(t, func() bool {
		return slices.ContainsFunc(adbCalls(p.tools), func(call string) bool { return strings.Contains(call, "forward --remove tcp:") })
	}, "the phone's forward being removed")
	servers, _ := os.ReadFile(filepath.Join(p.tools, "servers"))
	for _, pid := range strings.Fields(string(servers)) {
		number, _ := strconv.Atoi(pid)
		eventually(t, func() bool { return syscall.Kill(number, 0) != nil }, "the scrcpy server's shell ending")
	}
	if strings.Contains(strings.Join(adbCalls(p.tools), "\n"), "-s emulator-5556 push") {
		t.Error("the discovered emulator was also run as a phone")
	}
}

func TestPhoneWithoutVideoYet(t *testing.T) {
	p := phoneBridge(t, phoneUSB)
	p.scrcpy.nextVideo(t)
	p.waitForWithin(t, attachPatience, func(state State) bool { return state.Connected })

	if code := postInput(t, p.harness, "application/json", `{"type":"touch","phase":"down","x":0.5,"y":0.5}`); code != http.StatusBadGateway {
		t.Errorf("a touch before the first config packet answered %d, want 502", code)
	}
}
