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

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/emulator"
)

//go:embed index.html
var page []byte

const (
	frameHeaderBytes = 12
	inputTimeout     = 5 * time.Second
	maxInputBytes    = 64 << 10
)

// Listen opens the bridge's port on the loopback interface only; port 0 picks a free one.
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

// Handler serves the live view (/), its socket (/live), /status and /input.
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
	routes.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		json.NewEncoder(w).Encode(b.State())
	})
	routes.HandleFunc("POST /input", b.serveInput)
	routes.HandleFunc("GET /live", b.serveLive)

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

func (b *Bridge) serveInput(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "input must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInputBytes))
	if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
		http.Error(w, "input is too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		http.Error(w, "cannot read the input: "+err.Error(), http.StatusBadRequest)
		return
	}
	var message Message
	if err := json.Unmarshal(data, &message); err != nil {
		http.Error(w, "input is not valid JSON: "+err.Error(), http.StatusBadRequest)
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
	var sentFrame *emulator.Frame
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

func writeFrame(ctx context.Context, socket *websocket.Conn, frame *emulator.Frame) error {
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
