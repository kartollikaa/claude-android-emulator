package live

import (
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/device"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/sdk"
)

//go:embed index.html
var page []byte

const (
	frameHeaderBytes = 12
	inputTimeout     = 5 * time.Second
	sdkTimeout       = 10 * time.Second
	maxInputBytes    = 64 << 10
)

// Listen opens the bridge's port on the loopback interface only; port 0 picks a free one.
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

// Handler serves the live view (/), its socket (/live), /status, /input, the AVDs and their start, the toolbar and the settings.
func (b *Bridge) Handler() http.Handler {
	routes := http.NewServeMux()
	routes.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		w.Header().Set("cache-control", "no-store")
		// A framed page would let another site steer clicks onto the emulator.
		w.Header().Set("content-security-policy", "frame-ancestors 'none'")
		w.Header().Set("x-frame-options", "DENY")
		w.Write(page)
	})
	routes.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), statusWaitTimeout)
		defer cancel()
		state := b.LookedState(ctx)
		if r.URL.Query().Get("wait") == "booted" {
			state = b.WaitBooted(ctx)
		}
		w.Header().Set("content-type", "application/json")
		json.NewEncoder(w).Encode(state)
	})
	routes.HandleFunc("POST /input", b.serveInput)
	routes.HandleFunc("GET /live", b.serveLive)
	routes.HandleFunc("GET /avds", b.serveAVDs)
	routes.HandleFunc("POST /emulators/start", b.serveStart)
	routes.HandleFunc("POST /shutdown", b.serveShutdown)
	routes.HandleFunc("POST /rotate", serveAction(inputTimeout, func(ctx context.Context, request rotateRequest) (any, error) {
		return nil, b.Rotate(ctx, request.Direction)
	}))
	routes.HandleFunc("POST /screenshot", serveAction(inputTimeout, answeringPath(b.Screenshot)))
	routes.HandleFunc("POST /recording/start", serveAction(sdkTimeout, answeringNothing(b.StartRecording)))
	routes.HandleFunc("POST /recording/stop", serveAction(recordingStopTimeout, answeringPath(b.StopRecording)))
	routes.HandleFunc("POST /app-settings", serveAction(sdkTimeout, answeringNothing(b.OpenAppSettings)))
	routes.HandleFunc("GET /screen", serveRead(inputTimeout, b.Screen))
	routes.HandleFunc("POST /gesture", serveAction(maxGestureTime+gestureRouteMargin, func(ctx context.Context, gesture Gesture) (any, error) {
		return nil, b.Gesture(ctx, gesture)
	}))
	routes.HandleFunc("POST /open-url", serveAction(sdkTimeout, func(ctx context.Context, request struct {
		URL string `json:"url"`
	}) (any, error) {
		return nil, b.OpenURL(ctx, request.URL)
	}))
	routes.HandleFunc("POST /launch", serveAction(launchAppTimeout, func(ctx context.Context, request LaunchRequest) (any, error) {
		name, err := b.LaunchApp(ctx, request)
		if err != nil {
			return nil, err
		}
		return map[string]string{"package": name}, nil
	}))
	routes.HandleFunc("POST /logs", serveAction(sdkTimeout, func(ctx context.Context, request struct {
		Package string `json:"package"`
		Lines   int    `json:"lines"`
	}) (any, error) {
		log, err := b.Logs(ctx, request.Package, request.Lines)
		if err != nil {
			return nil, err
		}
		return map[string]string{"log": log}, nil
	}))
	routes.HandleFunc("GET /settings", serveRead(sdkTimeout, b.ReadSettings))
	routes.HandleFunc("GET /settings/locales", serveRead(sdkTimeout, func(ctx context.Context) (any, error) {
		locales, err := b.DeviceLocales(ctx)
		if err != nil {
			return nil, err
		}
		return map[string][]string{"locales": locales}, nil
	}))
	routes.HandleFunc("POST /settings", serveAction(sdkTimeout, func(ctx context.Context, change SettingsChange) (any, error) {
		settings, err := b.ChangeSettings(ctx, change)
		return settings, err
	}))
	routes.HandleFunc("POST /settings/reset", serveAction(sdkTimeout, func(ctx context.Context, _ struct{}) (any, error) {
		settings, err := b.ResetSettings(ctx)
		return settings, err
	}))

	return guardLocal(routes)
}

// guardLocal refuses requests a web page on another site could make: a Host other than the
// bridge's own loopback address (DNS rebinding) or a foreign Origin.
func guardLocal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isOwnHost(r) || !isOwnOrigin(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isOwnHost(r *http.Request) bool {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return false
	}
	_, port, err := net.SplitHostPort(local.String())
	if err != nil {
		return false
	}
	for _, host := range []string{"127.0.0.1", "localhost"} {
		if r.Host == host+":"+port {
			return true
		}
	}

	return false
}

// A missing Origin is a client that is not a browser page, which no other site can drive.
func isOwnOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)

	return err == nil && parsed.Scheme == "http" && parsed.Host == r.Host
}

// readJSON decodes a JSON request body into v, or answers the error and returns false.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "the body must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInputBytes))
	if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
		http.Error(w, "the body is too large", http.StatusRequestEntityTooLarge)
		return false
	}
	if err != nil {
		http.Error(w, "cannot read the body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		http.Error(w, "the body is not valid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}

	return true
}

func (b *Bridge) serveInput(w http.ResponseWriter, r *http.Request) {
	var message Message
	if !readJSON(w, r, &message) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), inputTimeout)
	defer cancel()
	if err := b.Input(ctx, message); err != nil {
		http.Error(w, err.Error(), inputErrorStatus(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (b *Bridge) serveAVDs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), sdkTimeout)
	defer cancel()
	avds, err := b.AVDs(ctx)
	w.Header().Set("content-type", "application/json")
	if err != nil {
		w.WriteHeader(sdkErrorStatus(err))
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string][]AVD{"avds": avds})
}

func (b *Bridge) serveStart(w http.ResponseWriter, r *http.Request) {
	var request struct {
		AVD string `json:"avd"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	if request.AVD == "" {
		http.Error(w, "name the virtual device to start in avd", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sdkTimeout)
	defer cancel()
	if err := b.Start(ctx, request.AVD); err != nil {
		http.Error(w, err.Error(), sdkErrorStatus(err))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (b *Bridge) serveShutdown(w http.ResponseWriter, r *http.Request) {
	var request struct{}
	if !readJSON(w, r, &request) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), inputTimeout)
	defer cancel()
	if err := b.Shutdown(ctx); err != nil {
		http.Error(w, err.Error(), inputErrorStatus(err))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

type rotateRequest struct {
	Direction string `json:"direction"`
}

// serveAction decodes the JSON body, runs act within timeout and answers its result as JSON, or 204 when it has none.
func serveAction[Request any](timeout time.Duration, act func(context.Context, Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request Request
		if !readJSON(w, r, &request) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		answer, err := act(ctx, request)
		writeAnswer(w, answer, err)
	}
}

// serveRead answers a GET with what read returns, as JSON.
func serveRead[Answer any](timeout time.Duration, read func(context.Context) (Answer, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		answer, err := read(ctx)
		writeAnswer(w, answer, err)
	}
}

// writeAnswer writes err with its status, 204 for a nil answer, or the answer as JSON.
func writeAnswer(w http.ResponseWriter, answer any, err error) {
	if err != nil {
		http.Error(w, err.Error(), controlErrorStatus(err))
		return
	}
	if answer == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("content-type", "application/json")
	json.NewEncoder(w).Encode(answer)
}

func answeringNothing(act func(context.Context) error) func(context.Context, struct{}) (any, error) {
	return func(ctx context.Context, _ struct{}) (any, error) { return nil, act(ctx) }
}

func answeringPath(capture func(context.Context) (string, error)) func(context.Context, struct{}) (any, error) {
	return func(ctx context.Context, _ struct{}) (any, error) {
		path, err := capture(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]string{"path": path}, nil
	}
}

func controlErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrBadInput):
		return http.StatusBadRequest
	case errors.Is(err, ErrNoEmulator), errors.Is(err, ErrNoADB):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrRecording), errors.Is(err, ErrNotRecording), errors.Is(err, ErrHomeInFront),
		errors.Is(err, ErrNotRunning), errors.Is(err, ErrNoLauncherActivity):
		return http.StatusConflict
	case errors.Is(err, ErrLocaleUnavailable):
		return http.StatusNotImplemented
	}

	return http.StatusBadGateway
}

func sdkErrorStatus(err error) int {
	var noSDK *sdk.NotFoundError
	switch {
	case errors.As(err, &noSDK):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrUnknownAVD):
		return http.StatusNotFound
	case errors.Is(err, ErrBusy):
		return http.StatusConflict
	}

	return http.StatusInternalServerError
}

func inputErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrBadInput):
		return http.StatusBadRequest
	case errors.Is(err, ErrNoEmulator):
		return http.StatusServiceUnavailable
	}

	return http.StatusBadGateway
}

func (b *Bridge) serveLive(w http.ResponseWriter, r *http.Request) {
	socket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	socket.SetReadLimit(maxInputBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		defer cancel()
		b.readInput(ctx, socket)
	}()
	err = b.writeFrames(ctx, socket)
	if websocket.CloseStatus(err) == -1 && ctx.Err() == nil {
		b.config.Logger.Debug("live view dropped", "err", err)
	}
	socket.CloseNow()
}

func (b *Bridge) readInput(ctx context.Context, socket *websocket.Conn) {
	for {
		kind, data, err := socket.Read(ctx)
		if err != nil {
			return
		}
		var message Message
		if kind != websocket.MessageText || json.Unmarshal(data, &message) != nil {
			continue
		}
		inputCtx, cancel := context.WithTimeout(ctx, inputTimeout)
		if err := b.Input(inputCtx, message); err != nil && !errors.Is(err, ErrNoEmulator) {
			b.config.Logger.Warn("input not delivered", "type", message.Type, "err", err)
		}
		cancel()
	}
}

// writeFrames sends a JSON state message whenever the state changes and a binary frame
// (12-byte little-endian width, height, seq header, then RGB888 rows) whenever the frame does.
func (b *Bridge) writeFrames(ctx context.Context, socket *websocket.Conn) error {
	changes, stop := b.watch()
	defer stop()
	var sentState *State
	var sentFrame *device.Frame
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changes:
		}
		state, frame := b.snapshot()
		connection := state
		connection.Seq = 0
		if sentState == nil || *sentState != connection {
			message, _ := json.Marshal(struct {
				Type string `json:"type"`
				State
			}{"state", connection})
			if err := socket.Write(ctx, websocket.MessageText, message); err != nil {
				return err
			}
			sentState = &connection
		}
		if frame == nil || frame == sentFrame {
			continue
		}
		if err := writeFrame(ctx, socket, frame); err != nil {
			return err
		}
		sentFrame = frame
	}
}

func writeFrame(ctx context.Context, socket *websocket.Conn, frame *device.Frame) error {
	writer, err := socket.Writer(ctx, websocket.MessageBinary)
	if err != nil {
		return err
	}
	header := make([]byte, frameHeaderBytes)
	binary.LittleEndian.PutUint32(header[0:], uint32(frame.Width))
	binary.LittleEndian.PutUint32(header[4:], uint32(frame.Height))
	binary.LittleEndian.PutUint32(header[8:], frame.Seq)
	if _, err := writer.Write(header); err != nil {
		return err
	}
	if _, err := writer.Write(frame.RGB); err != nil {
		return err
	}

	return writer.Close()
}
