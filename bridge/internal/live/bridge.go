// Package live keeps one emulator connected and serves its screen and input to the live view.
package live

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/discovery"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/emulator"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/sdk"
)

// Config sets where the bridge looks for emulators and the SDK, and how it streams them.
type Config struct {
	DiscoveryDir  string
	StreamWidth   int
	RetryInterval time.Duration
	PollInterval  time.Duration
	LaunchTimeout time.Duration
	Logger        *slog.Logger
	SDKCandidates []string
	AVDHome       string
	LogDir        string
	// CapturesDir is where screenshots and screen recordings are saved.
	CapturesDir string
	Now         func() time.Time
}

// State is what the bridge knows about the emulator, as /status and the live view see it.
type State struct {
	Connected      bool              `json:"connected"`
	Booted         bool              `json:"booted"`
	Serial         string            `json:"serial"`
	AVD            string            `json:"avd"`
	Device         emulator.Size     `json:"device"`
	Stream         emulator.Size     `json:"stream"`
	Seq            uint32            `json:"seq"`
	Launch         Launch            `json:"launch,omitzero"`
	Rotation       emulator.Rotation `json:"rotation"`
	RecordingSince time.Time         `json:"recordingSince,omitzero"`
	// RecordingError says why the newest recording was not saved.
	RecordingError string `json:"recordingError,omitempty"`
	// Saved is the newest screenshot or recording saved, so the page can say where.
	Saved string `json:"saved,omitempty"`
}

// Message is one input from the live view or the mod; a touch's x and y are screen fractions.
type Message struct {
	Type  string   `json:"type"`
	Phase string   `json:"phase,omitempty"`
	X     *float64 `json:"x,omitempty"`
	Y     *float64 `json:"y,omitempty"`
	Key   string   `json:"key,omitempty"`
	Text  string   `json:"text,omitempty"`
}

var (
	// ErrNoEmulator is returned for input while no emulator is connected.
	ErrNoEmulator = errors.New("no emulator is connected")
	// ErrBadInput is returned for a message the bridge cannot turn into an emulator call.
	ErrBadInput = errors.New("bad input")
)

// Bridge holds the connection to the current emulator and fans its frames out to viewers.
type Bridge struct {
	config Config

	mu        sync.Mutex
	conn      *emulator.Conn
	state     State
	launch    Launch
	launchID  uint64
	latest    *emulator.Frame
	viewers   map[chan struct{}]struct{}
	recording *recording
}

// New makes a bridge; Run connects it.
func New(config Config) *Bridge {
	if config.StreamWidth <= 0 {
		config.StreamWidth = 540
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = time.Second
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 500 * time.Millisecond
	}
	if config.LaunchTimeout <= 0 {
		config.LaunchTimeout = 3 * time.Minute
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	home, _ := os.UserHomeDir()
	if config.SDKCandidates == nil {
		config.SDKCandidates = sdk.Candidates(os.Getenv, home, runtime.GOOS)
	}
	if config.AVDHome == "" {
		config.AVDHome = sdk.AVDHome(os.Getenv, home)
	}
	if config.LogDir == "" {
		config.LogDir = filepath.Join(os.TempDir(), "claude-android-emulator")
	}
	if config.CapturesDir == "" {
		config.CapturesDir = defaultCapturesDir(home)
	}
	if config.Now == nil {
		config.Now = time.Now
	}

	return &Bridge{config: config, viewers: map[chan struct{}]struct{}{}}
}

// Run keeps an emulator connected until ctx ends, starting over whenever it goes away.
func (b *Bridge) Run(ctx context.Context) {
	for ctx.Err() == nil {
		conn := b.connect(ctx)
		if conn == nil {
			sleep(ctx, b.config.RetryInterval)
			continue
		}
		size := conn.StreamBox(b.config.StreamWidth)
		b.attach(conn)
		b.config.Logger.Info("emulator connected", "serial", conn.Emulator.Serial, "device", conn.Device, "stream", size)
		streaming, stopStreaming := context.WithCancel(ctx)
		go b.watchBoot(streaming, conn)
		err := conn.Stream(streaming, size, b.publish)
		stopStreaming()
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
		conn, err := emulator.Dial(ctx, candidate, b.config.Logger)
		if err == nil {
			return conn
		}
		b.config.Logger.Debug("emulator did not answer", "serial", candidate.Serial, "err", err)
	}

	return nil
}

func (b *Bridge) attach(conn *emulator.Conn) {
	b.mu.Lock()
	b.conn = conn
	b.latest = nil
	b.state = b.keepingCaptures(State{Connected: true, Serial: conn.Emulator.Serial, AVD: conn.Emulator.AVD, Device: conn.Device})
	if b.launch.Phase == launchFailed || b.launch.AVD == conn.Emulator.AVD {
		b.launch = Launch{}
	}
	b.mu.Unlock()
	b.notifyViewers()
}

func (b *Bridge) watchBoot(ctx context.Context, conn *emulator.Conn) {
	for ctx.Err() == nil {
		isBooted, err := conn.Booted(ctx)
		if err != nil {
			b.config.Logger.Debug("boot status unavailable", "err", err)
		}
		if isBooted {
			b.mu.Lock()
			isCurrent := b.conn == conn
			if isCurrent {
				b.state.Booted = true
			}
			b.mu.Unlock()
			if isCurrent {
				b.notifyViewers()
			}
			return
		}
		sleep(ctx, b.config.PollInterval)
	}
}

func (b *Bridge) detach() {
	b.mu.Lock()
	b.conn = nil
	b.latest = nil
	b.state = b.keepingCaptures(State{})
	b.mu.Unlock()
	b.notifyViewers()
}

// keepingCaptures carries the recording and the newest saved file over a reconnect, which does not end a recording.
func (b *Bridge) keepingCaptures(state State) State {
	state.RecordingSince, state.RecordingError, state.Saved = b.state.RecordingSince, b.state.RecordingError, b.state.Saved

	return state
}

func (b *Bridge) publish(frame emulator.Frame) {
	b.mu.Lock()
	b.latest = &frame
	b.state.Seq = frame.Seq
	if frame.Width > 0 && frame.Height > 0 {
		b.state.Stream = emulator.Size{Width: frame.Width, Height: frame.Height}
		b.state.Rotation = frame.Rotation
	}
	b.mu.Unlock()
	b.notifyViewers()
}

func (b *Bridge) State() State {
	state, _ := b.snapshot()
	return state
}

func (b *Bridge) snapshot() (State, *emulator.Frame) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.state
	state.Launch = b.launch
	return state, b.latest
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

func (b *Bridge) Input(ctx context.Context, message Message) error {
	deliver, err := message.delivery()
	if err != nil {
		return err
	}
	conn := b.connection()
	if conn == nil {
		return ErrNoEmulator
	}

	return deliver(ctx, conn)
}

// delivery checks the message and returns the emulator call it stands for.
func (m Message) delivery() (func(context.Context, *emulator.Conn) error, error) {
	switch m.Type {
	case "touch":
		if m.Phase != "down" && m.Phase != "move" && m.Phase != "up" {
			return nil, fmt.Errorf("%w: unknown touch phase %q", ErrBadInput, m.Phase)
		}
		if m.X == nil || m.Y == nil {
			return nil, fmt.Errorf("%w: a touch needs x and y", ErrBadInput)
		}
		return func(ctx context.Context, conn *emulator.Conn) error {
			return conn.Touch(ctx, *m.X, *m.Y, m.Phase != "up")
		}, nil
	case "key":
		if m.Key == "" {
			return nil, fmt.Errorf("%w: a key message names no key", ErrBadInput)
		}
		return func(ctx context.Context, conn *emulator.Conn) error { return conn.Key(ctx, m.Key) }, nil
	case "text":
		if m.Text == "" {
			return nil, fmt.Errorf("%w: a text message carries no text", ErrBadInput)
		}
		return func(ctx context.Context, conn *emulator.Conn) error { return conn.Text(ctx, m.Text) }, nil
	}

	return nil, fmt.Errorf("%w: unknown input type %q", ErrBadInput, m.Type)
}

func sleep(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
