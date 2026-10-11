// Package live keeps one emulator connected and serves its screen and input to the live view.
package live

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/discovery"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/emulator"
)

// Config sets where the bridge looks for emulators and how it streams them.
type Config struct {
	DiscoveryDir  string
	StreamWidth   int
	RetryInterval time.Duration
	Logger        *slog.Logger
}

// State is what the bridge knows about the emulator, as /status and the live view see it.
type State struct {
	Connected bool          `json:"connected"`
	Serial    string        `json:"serial"`
	Device    emulator.Size `json:"device"`
	Stream    emulator.Size `json:"stream"`
	Seq       uint32        `json:"seq"`
}

// Message is one input from the live view or the mod.
type Message struct {
	Type  string  `json:"type"`
	Phase string  `json:"phase,omitempty"`
	X     float64 `json:"x,omitempty"`
	Y     float64 `json:"y,omitempty"`
	Key   string  `json:"key,omitempty"`
	Text  string  `json:"text,omitempty"`
}

// ErrNoEmulator is returned for input while no emulator is connected.
var ErrNoEmulator = errors.New("no emulator is connected")

// Bridge holds the connection to the current emulator and fans its frames out to viewers.
type Bridge struct {
	config Config

	mu      sync.Mutex
	conn    *emulator.Conn
	state   State
	latest  *emulator.Frame
	viewers map[chan struct{}]struct{}
}

// New makes a bridge; Run connects it.
func New(config Config) *Bridge {
	if config.StreamWidth <= 0 {
		config.StreamWidth = 540
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = time.Second
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	return &Bridge{config: config, viewers: map[chan struct{}]struct{}{}}
}

// Run keeps an emulator connected until ctx ends: it finds one, streams it, and starts over
// when it goes away.
func (b *Bridge) Run(ctx context.Context) {
	for ctx.Err() == nil {
		conn := b.connect(ctx)
		if conn == nil {
			sleep(ctx, b.config.RetryInterval)
			continue
		}
		size := conn.StreamSize(b.config.StreamWidth)
		b.attach(conn, size)
		b.config.Logger.Info("emulator connected", "serial", conn.Emulator.Serial, "device", conn.Device, "stream", size)
		err := conn.Stream(ctx, size, b.publish)
		b.detach()
		conn.Close()
		if ctx.Err() == nil {
			b.config.Logger.Info("emulator went away", "serial", conn.Emulator.Serial, "reason", err)
			sleep(ctx, b.config.RetryInterval)
		}
	}
}

func (b *Bridge) connect(ctx context.Context) *emulator.Conn {
	found, err := discovery.Find(b.config.DiscoveryDir)
	if err != nil {
		b.config.Logger.Warn("cannot read the emulator discovery dir", "dir", b.config.DiscoveryDir, "err", err)
		return nil
	}
	// A crashed emulator leaves its file behind, so try each until one answers.
	for _, candidate := range found {
		conn, err := emulator.Dial(ctx, candidate)
		if err == nil {
			return conn
		}
		b.config.Logger.Debug("emulator did not answer", "serial", candidate.Serial, "err", err)
	}

	return nil
}

func (b *Bridge) attach(conn *emulator.Conn, size emulator.Size) {
	b.mu.Lock()
	b.conn = conn
	b.latest = nil
	b.state = State{Connected: true, Serial: conn.Emulator.Serial, Device: conn.Device, Stream: size}
	b.mu.Unlock()
	b.notifyViewers()
}

func (b *Bridge) detach() {
	b.mu.Lock()
	b.conn = nil
	b.latest = nil
	b.state = State{}
	b.mu.Unlock()
	b.notifyViewers()
}

func (b *Bridge) publish(frame emulator.Frame) {
	b.mu.Lock()
	b.latest = &frame
	b.state.Seq = frame.Seq
	b.mu.Unlock()
	b.notifyViewers()
}

// State reports the current emulator state.
func (b *Bridge) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *Bridge) snapshot() (State, *emulator.Frame) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state, b.latest
}

// watch registers a viewer: its channel fires whenever the state or the frame changes,
// at most one pending signal at a time so a slow viewer skips to the newest frame.
func (b *Bridge) watch() (changes chan struct{}, stop func()) {
	changes = make(chan struct{}, 1)
	changes <- struct{}{}
	b.mu.Lock()
	b.viewers[changes] = struct{}{}
	b.mu.Unlock()

	return changes, func() {
		b.mu.Lock()
		delete(b.viewers, changes)
		b.mu.Unlock()
	}
}

func (b *Bridge) notifyViewers() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for viewer := range b.viewers {
		select {
		case viewer <- struct{}{}:
		default:
		}
	}
}

// Input forwards one message to the emulator.
func (b *Bridge) Input(ctx context.Context, message Message) error {
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	if conn == nil {
		return ErrNoEmulator
	}
	switch message.Type {
	case "touch":
		switch message.Phase {
		case "down", "move":
			return conn.Touch(ctx, message.X, message.Y, true)
		case "up":
			return conn.Touch(ctx, message.X, message.Y, false)
		}
		return fmt.Errorf("unknown touch phase %q", message.Phase)
	case "key":
		return conn.Key(ctx, message.Key)
	case "text":
		return conn.Text(ctx, message.Text)
	}

	return fmt.Errorf("unknown input type %q", message.Type)
}

func sleep(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
