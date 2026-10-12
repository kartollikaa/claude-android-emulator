package live

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/device"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/discovery"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/proc"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/sdk"
)

const (
	launchStarting = "starting"
	launchFailed   = "failed"
)

// Launch is an emulator the bridge booted that it has not connected to yet.
type Launch struct {
	AVD   string `json:"avd"`
	Phase string `json:"phase"`
	Error string `json:"error,omitempty"`
}

// AVD is a virtual device the SDK can boot, and whether an emulator runs it now.
type AVD struct {
	sdk.AVD
	Running bool `json:"running"`
}

var (
	// ErrUnknownAVD is returned for a start of an AVD the SDK does not list.
	ErrUnknownAVD = errors.New("the Android SDK has no such virtual device")
	// ErrBusy is returned for a start while an emulator is connected or starting; a phone shown gives way to it.
	ErrBusy = errors.New("cannot start another emulator")
)

// AVDs lists the virtual devices the SDK can boot; with no SDK it returns an *sdk.NotFoundError.
func (b *Bridge) AVDs(ctx context.Context) ([]AVD, error) {
	found, err := sdk.Find(b.config.SDKCandidates, runtime.GOOS)
	if err != nil {
		return nil, err
	}
	return b.listAVDs(ctx, found)
}

func (b *Bridge) listAVDs(ctx context.Context, found sdk.SDK) ([]AVD, error) {
	avds, err := found.AVDs(ctx, b.config.AVDHome)
	if err != nil {
		return nil, err
	}
	running, err := discovery.RunningAVDs(b.config.DiscoveryDir)
	if err != nil {
		return nil, err
	}
	listed := make([]AVD, 0, len(avds))
	for _, avd := range avds {
		listed = append(listed, AVD{AVD: avd, Running: running[avd.Name]})
	}

	return listed, nil
}

// Start boots the emulator for avd with gRPC on a free port; the state's launch follows it.
func (b *Bridge) Start(ctx context.Context, avd string) error {
	found, err := sdk.Find(b.config.SDKCandidates, runtime.GOOS)
	if err != nil {
		return err
	}
	avds, err := b.listAVDs(ctx, found)
	if err != nil {
		return err
	}
	index := slices.IndexFunc(avds, func(listed AVD) bool { return listed.Name == avd })
	if index < 0 {
		return fmt.Errorf("%w: %q", ErrUnknownAVD, avd)
	}
	id, err := b.reserveLaunch(avd, avds[index].Running)
	if err != nil {
		return err
	}
	logPath := filepath.Join(b.config.LogDir, "emulator-"+avd+".log")
	pid, err := bootOnFreePort(found, avd, logPath)
	if err != nil {
		b.endLaunch(id, Launch{})
		return err
	}
	b.config.Logger.Info("emulator starting", "avd", avd, "pid", pid, "log", logPath)
	go b.awaitExit(id, pid, avd, logPath)
	time.AfterFunc(b.config.LaunchTimeout, func() { b.giveUp(id, pid, avd, logPath) })

	return nil
}

// reserveLaunch claims the one launch slot for avd and returns the launch's id.
func (b *Bridge) reserveLaunch(avd string, isRunning bool) (uint64, error) {
	b.mu.Lock()
	switch {
	case b.state.Connected && b.state.Kind == device.Emulator:
		b.mu.Unlock()
		return 0, fmt.Errorf("%w: an emulator is already running", ErrBusy)
	case b.launch.Phase == launchStarting:
		starting := b.launch.AVD
		b.mu.Unlock()
		return 0, fmt.Errorf("%w: %s is starting", ErrBusy, starting)
	case isRunning:
		b.mu.Unlock()
		return 0, fmt.Errorf("%w: %s is already running", ErrBusy, avd)
	}
	b.launchID++
	id := b.launchID
	b.launch = Launch{AVD: avd, Phase: launchStarting}
	b.mu.Unlock()
	b.notifyViewers()

	return id, nil
}

// endLaunch replaces launch id, if it is still starting, with next; it reports whether it did.
func (b *Bridge) endLaunch(id uint64, next Launch) bool {
	b.mu.Lock()
	isCurrent := b.isStartingLocked(id)
	if isCurrent {
		b.launch = next
	}
	b.mu.Unlock()
	if isCurrent {
		b.notifyViewers()
	}

	return isCurrent
}

func bootOnFreePort(found sdk.SDK, avd, logPath string) (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	return found.Boot(avd, port, logPath)
}

// awaitExit reports a launch as failed when its emulator exits before the bridge connects to it.
func (b *Bridge) awaitExit(id uint64, pid int, avd, logPath string) {
	for b.isStarting(id) {
		if !proc.Alive(pid) {
			if b.endLaunch(id, Launch{AVD: avd, Phase: launchFailed, Error: sdk.LastError(logPath)}) {
				b.config.Logger.Warn("emulator exited while starting", "avd", avd, "log", logPath)
			}
			return
		}
		time.Sleep(b.config.PollInterval)
	}
}

func (b *Bridge) isStarting(id uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.isStartingLocked(id)
}

// isStartingLocked reports whether launch id is the current one and still starting; the caller holds b.mu.
func (b *Bridge) isStartingLocked(id uint64) bool {
	return b.launchID == id && b.launch.Phase == launchStarting
}

// giveUp stops an emulator that never came up, such as one whose gRPC port was taken meanwhile.
func (b *Bridge) giveUp(id uint64, pid int, avd, logPath string) {
	reason := fmt.Sprintf("%s did not come up within %s; its log is %s", avd, b.config.LaunchTimeout, logPath)
	if b.endLaunch(id, Launch{AVD: avd, Phase: launchFailed, Error: reason}) {
		b.config.Logger.Warn("stopping an emulator that did not come up", "avd", avd, "log", logPath)
		sdk.Stop(pid)
	}
}

// Shutdown asks the connected emulator to power off; the bridge then sees it go away.
func (b *Bridge) Shutdown(ctx context.Context) error {
	conn := b.connection()
	if conn == nil {
		return ErrNoEmulator
	}

	return conn.Shutdown(ctx)
}
