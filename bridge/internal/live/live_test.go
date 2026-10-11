package live

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/emulator"
	pb "github.com/kartollikaa/claude-android-emulator/bridge/internal/emulatorpb"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/fakeemulator"
)

const (
	deviceWidth  = 8
	deviceHeight = 16
	streamWidth  = 4
	streamHeight = 8
	patience     = 3 * time.Second
)

func deviceSize() emulator.Size { return emulator.Size{Width: deviceWidth, Height: deviceHeight} }
func streamSize() emulator.Size { return emulator.Size{Width: streamWidth, Height: streamHeight} }

type harness struct {
	dir    string
	server *httptest.Server
	bridge *Bridge
}

func startBridge(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	bridge := New(Config{
		DiscoveryDir:  dir,
		StreamWidth:   streamWidth,
		RetryInterval: 20 * time.Millisecond,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	go bridge.Run(ctx)
	server := httptest.NewServer(bridge.Handler())
	t.Cleanup(func() {
		server.Close()
		cancel()
	})

	return &harness{dir: dir, server: server, bridge: bridge}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	t.Cleanup(cancel)
	return ctx
}

func (h *harness) waitFor(t *testing.T, condition func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		if state := h.status(t); condition(state) {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("status never reached the expected state; last %+v", h.status(t))
	return State{}
}

func (h *harness) status(t *testing.T) State {
	t.Helper()
	response, err := http.Get(h.server.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("/status answered %d", response.StatusCode)
	}
	var state State
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func (h *harness) dialLive(t *testing.T, ctx context.Context) *websocket.Conn {
	t.Helper()
	socket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	socket.SetReadLimit(1 << 20)
	t.Cleanup(func() { socket.CloseNow() })
	return socket
}

// connected also returns a sender into the screenshot stream the bridge opened.
func (h *harness) connected(t *testing.T, ctx context.Context, token string) (*fakeemulator.Fake, chan<- *pb.Image) {
	t.Helper()
	fake := fakeemulator.Start(t, deviceWidth, deviceHeight, token)
	fake.WriteINI(t, h.dir, 1)
	frames := fake.NextStream(t, ctx)
	h.waitFor(t, func(state State) bool { return state.Connected })
	return fake, frames
}

func readFrame(t *testing.T, ctx context.Context, socket *websocket.Conn) (width, height, seq uint32, rgb []byte) {
	t.Helper()
	for {
		kind, data, err := socket.Read(ctx)
		if err != nil {
			t.Fatalf("no frame arrived: %v", err)
		}
		if kind != websocket.MessageBinary {
			continue
		}
		if len(data) < frameHeaderBytes {
			t.Fatalf("frame of %d bytes has no header", len(data))
		}
		return binary.LittleEndian.Uint32(data[0:]), binary.LittleEndian.Uint32(data[4:]), binary.LittleEndian.Uint32(data[8:]), data[frameHeaderBytes:]
	}
}

func sendJSON(t *testing.T, ctx context.Context, socket *websocket.Conn, message string) {
	t.Helper()
	if err := socket.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, condition func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never happened", what)
}

func TestLiveStreamsFrames(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	_, frames := h.connected(t, ctx, "")
	socket := h.dialLive(t, ctx)

	sent := fakeemulator.Frame(42, streamWidth, streamHeight, 10)
	frames <- sent

	frameCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	width, height, seq, rgb := readFrame(t, frameCtx, socket)
	if width != streamWidth || height != streamHeight || seq != 42 {
		t.Fatalf("header = %dx%d seq %d, want %dx%d seq 42", width, height, seq, streamWidth, streamHeight)
	}
	if !bytes.Equal(rgb, sent.GetImage()) {
		t.Fatalf("got %d pixel bytes that differ from the %d the emulator sent", len(rgb), len(sent.GetImage()))
	}
}

func TestLiveTouchReachesTheEmulator(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	fake, _ := h.connected(t, ctx, "")
	socket := h.dialLive(t, ctx)

	for _, phase := range []string{"down", "move", "up"} {
		sendJSON(t, ctx, socket, `{"type":"touch","phase":"`+phase+`","x":0.5,"y":0.25}`)
	}

	eventually(t, func() bool { return len(fake.Touches()) == 3 }, "three touches reaching the emulator")
	wantX, wantY := int32(4), int32(4) // round(0.5*7), round(0.25*15)
	for i, touch := range fake.Touches() {
		if touch.GetX() != wantX || touch.GetY() != wantY {
			t.Errorf("touch %d at %d,%d, want %d,%d", i, touch.GetX(), touch.GetY(), wantX, wantY)
		}
		if isUp := i == 2; isUp != (touch.GetPressure() == 0) {
			t.Errorf("touch %d pressure %d: down and move press, up releases", i, touch.GetPressure())
		}
	}
}

func TestLiveKeysReachTheEmulator(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	fake, _ := h.connected(t, ctx, "")
	socket := h.dialLive(t, ctx)

	sendJSON(t, ctx, socket, `{"type":"key","key":"GoHome"}`)
	sendJSON(t, ctx, socket, `{"type":"text","text":"a"}`)

	eventually(t, func() bool { return len(fake.Keys()) == 2 }, "two key events reaching the emulator")
	keys := fake.Keys()
	if keys[0].GetEventType() != pb.KeyboardEvent_keypress || keys[0].GetKey() != "GoHome" {
		t.Errorf("first key event = %v, want a GoHome keypress", keys[0])
	}
	if keys[1].GetEventType() != pb.KeyboardEvent_keypress || keys[1].GetText() != "a" {
		t.Errorf("second key event = %v, want a keypress typing a", keys[1])
	}
}

func postInput(t *testing.T, h *harness, contentType, body string) int {
	t.Helper()
	response, err := http.Post(h.server.URL+"/input", contentType, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response.StatusCode
}

func TestHTTPInput(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	fake, _ := h.connected(t, ctx, "")

	statuses := []int{
		postInput(t, h, "application/json", `{"type":"touch","phase":"down","x":0.5,"y":0.25}`),
		postInput(t, h, "application/json; charset=utf-8", `{"type":"touch","phase":"up","x":0.5,"y":0.25}`),
		postInput(t, h, "application/json", `{"type":"key","key":"GoBack"}`),
		postInput(t, h, "application/json", `{"type":"text","text":"hi"}`),
	}

	for i, status := range statuses {
		if status != http.StatusNoContent {
			t.Errorf("input %d answered %d, want 204", i, status)
		}
	}
	touches, keys := fake.Touches(), fake.Keys()
	if len(touches) != 2 || touches[0].GetPressure() == 0 || touches[1].GetPressure() != 0 || touches[0].GetX() != 4 {
		t.Errorf("touches = %v, want a press then a release at x 4", touches)
	}
	if len(keys) != 2 || keys[0].GetKey() != "GoBack" || keys[1].GetText() != "hi" {
		t.Errorf("keys = %v, want GoBack then the text hi", keys)
	}
}

func TestHTTPInputRejectsOtherContentTypes(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	fake, _ := h.connected(t, ctx, "")

	for _, contentType := range []string{"text/plain", "application/x-www-form-urlencoded", ""} {
		if status := postInput(t, h, contentType, `{"type":"key","key":"GoHome"}`); status != http.StatusUnsupportedMediaType {
			t.Errorf("content type %q answered %d, want 415", contentType, status)
		}
	}
	if keys := fake.Keys(); len(keys) != 0 {
		t.Fatalf("%d key events reached the emulator", len(keys))
	}
}

var badMessages = []string{
	`{"type":"key","key":"GoHome"} x`,
	`{"type":"swipe"}`,
	`{"type":"touch","phase":"hover","x":0.5,"y":0.5}`,
	`{"type":"touch","phase":"down"}`,
	`{"type":"key"}`,
	`{"type":"text"}`,
}

func TestHTTPInputRejectsBadMessages(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	if status := postInput(t, h, "application/json", badMessages[1]); status != http.StatusBadRequest {
		t.Errorf("a bad message with no emulator answered %d, want 400", status)
	}
	fake, _ := h.connected(t, ctx, "")

	for _, message := range badMessages {
		if status := postInput(t, h, "application/json", message); status != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400", message, status)
		}
	}
	if status := postInput(t, h, "application/json", strings.Repeat(" ", maxInputBytes+1)); status != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversized body answered %d, want 413", status)
	}
	if touches, keys := fake.Touches(), fake.Keys(); len(touches)+len(keys) != 0 {
		t.Fatalf("bad messages reached the emulator: touches %v, keys %v", touches, keys)
	}
}

func TestLiveDropsBadMessages(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	fake, _ := h.connected(t, ctx, "")
	socket := h.dialLive(t, ctx)

	for _, message := range badMessages {
		sendJSON(t, ctx, socket, message)
	}
	sendJSON(t, ctx, socket, `{"type":"key","key":"GoBack"}`)

	eventually(t, func() bool { return len(fake.Keys()) > 0 }, "the good message reaching the emulator")
	if touches, keys := fake.Touches(), fake.Keys(); len(touches) != 0 || len(keys) != 1 || keys[0].GetKey() != "GoBack" {
		t.Fatalf("touches %v, keys %v; want only the GoBack sent after the bad messages", touches, keys)
	}
}

func TestStatusConnected(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	_, frames := h.connected(t, ctx, "")
	frames <- fakeemulator.Frame(7, streamWidth, streamHeight, 0)

	state := h.waitFor(t, func(state State) bool { return state.Seq == 7 })

	want := State{
		Connected: true,
		Serial:    "emulator-5556",
		Device:    deviceSize(),
		Stream:    streamSize(),
		Seq:       7,
	}
	if state != want {
		t.Fatalf("status = %+v, want %+v", state, want)
	}
}

func TestStatusStreamFollowsTheFrames(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	_, frames := h.connected(t, ctx, "")
	if state := h.status(t); state.Stream != (emulator.Size{}) {
		t.Fatalf("stream = %+v before any frame arrived, want none", state.Stream)
	}

	frames <- fakeemulator.Frame(3, streamWidth, streamHeight-1, 0)

	state := h.waitFor(t, func(state State) bool { return state.Seq == 3 })
	if want := (emulator.Size{Width: streamWidth, Height: streamHeight - 1}); state.Stream != want {
		t.Fatalf("stream = %+v, want the %+v the emulator sent rather than the size asked for", state.Stream, want)
	}
}

func TestDisplayOffKeepsTheStreamSize(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	_, frames := h.connected(t, ctx, "")
	socket := h.dialLive(t, ctx)
	frames <- fakeemulator.Frame(1, streamWidth, streamHeight, 0)
	readFrame(t, ctx, socket)

	frames <- fakeemulator.Frame(2, 0, 0, 0)

	width, height, seq, rgb := readFrame(t, ctx, socket)
	if width != 0 || height != 0 || seq != 2 || len(rgb) != 0 {
		t.Fatalf("display-off frame = %dx%d seq %d with %d bytes, want an empty 0x0 frame seq 2", width, height, seq, len(rgb))
	}
	if state := h.status(t); state.Stream != streamSize() {
		t.Fatalf("stream = %+v after the display went off, want the last picture's %+v", state.Stream, streamSize())
	}
}

func TestLiveStateMatchesStatus(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	_, frames := h.connected(t, ctx, "")
	frames <- fakeemulator.Frame(5, streamWidth, streamHeight, 0)
	want := h.waitFor(t, func(state State) bool { return state.Seq == 5 })
	want.Seq = 0
	socket := h.dialLive(t, ctx)

	for {
		kind, data, err := socket.Read(ctx)
		if err != nil {
			t.Fatalf("no state message arrived: %v", err)
		}
		if kind != websocket.MessageText {
			continue
		}
		var got State
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("state message = %+v, want /status without seq %+v", got, want)
		}
		return
	}
}

func TestPageRefusesToBeFramed(t *testing.T) {
	h := startBridge(t)

	response, err := http.Get(h.server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	if policy := response.Header.Get("Content-Security-Policy"); policy != "frame-ancestors 'none'" {
		t.Errorf("Content-Security-Policy = %q, want frame-ancestors 'none'", policy)
	}
	if frameOptions := response.Header.Get("X-Frame-Options"); frameOptions != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", frameOptions)
	}
}

func TestStatusWithoutEmulator(t *testing.T) {
	h := startBridge(t)
	time.Sleep(60 * time.Millisecond)

	if state := h.status(t); state.Connected {
		t.Fatalf("status = %+v with no emulator running", state)
	}
}

func TestTokenIsSentOnEveryCall(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	fake, frames := h.connected(t, ctx, "s3cret==")
	socket := h.dialLive(t, ctx)
	frames <- fakeemulator.Frame(1, streamWidth, streamHeight, 0)
	readFrame(t, ctx, socket)
	sendJSON(t, ctx, socket, `{"type":"key","key":"GoHome"}`)
	eventually(t, func() bool { return len(fake.Keys()) == 1 }, "the key reaching the emulator")

	calls, rejected := fake.Calls()
	if rejected != 0 || calls < 3 {
		t.Fatalf("%d of %d calls rejected; want every call (screenshot, stream, key) authorized", rejected, calls)
	}

	stranger := fakeemulator.Start(t, deviceWidth, deviceHeight, "s3cret==")
	tokenless := startBridge(t)
	if err := os.WriteFile(tokenless.dir+"/pid_9.ini", []byte("grpc.port="+strconv.Itoa(stranger.Port)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { _, rejected := stranger.Calls(); return rejected > 0 }, "the fake refusing a call without the token")
	if tokenless.status(t).Connected {
		t.Fatal("a bridge without the token connected: the check above proves nothing")
	}
}

func TestReconnectsAfterTheEmulatorRestarts(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	first, frames := h.connected(t, ctx, "")
	socket := h.dialLive(t, ctx)
	frames <- fakeemulator.Frame(1, streamWidth, streamHeight, 0)
	readFrame(t, ctx, socket)

	first.Stop()
	os.Remove(h.dir + "/pid_1.ini")
	h.waitFor(t, func(state State) bool { return !state.Connected })

	second := fakeemulator.Start(t, deviceWidth, deviceHeight, "")
	second.WriteINI(t, h.dir, 2)
	again := second.NextStream(t, ctx)
	again <- fakeemulator.Frame(1, streamWidth, streamHeight, 99)

	_, _, _, rgb := readFrame(t, ctx, socket)
	if rgb[0] != 99 {
		t.Fatalf("the open socket got a frame starting %d, want the new emulator's 99", rgb[0])
	}
}

func TestRejectsForeignOrigin(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	_, _ = h.connected(t, ctx, "")

	_, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/live", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: err %v, response %v; want 403", err, response)
	}

	own, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/live", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{h.server.URL}},
	})
	if err != nil {
		t.Fatalf("the bridge's own origin was refused: %v", err)
	}
	own.CloseNow()
}

func TestRejectsForeignHost(t *testing.T) {
	h := startBridge(t)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(h.server.URL, "http://"))

	for host, want := range map[string]int{
		"evil.example:" + port: http.StatusForbidden,
		"127.0.0.1:1":          http.StatusForbidden,
		"[::1]:" + port:        http.StatusForbidden,
		"localhost:" + port:    http.StatusOK,
		"127.0.0.1:" + port:    http.StatusOK,
	} {
		request, _ := http.NewRequest(http.MethodGet, h.server.URL+"/status", nil)
		request.Host = host
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Errorf("Host %s answered %d, want %d", host, response.StatusCode, want)
		}
	}
}

func TestListensOnLoopbackOnly(t *testing.T) {
	listener, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	address := listener.Addr().(*net.TCPAddr)
	if !address.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listening on %v, want 127.0.0.1 only", address.IP)
	}
}
