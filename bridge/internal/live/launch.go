package live

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/discovery"
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
	// ErrBusy is returned for a start while an emulator runs or another one is starting.
	ErrBusy = errors.New("cannot start another emulator")
)

// AVDs lists the virtual devices the SDK can boot; with no SDK it returns an *sdk.NotFoundError.
func (b *Bridge) AVDs(ctx context.Context) ([]AVD, error) {
	found, err := sdk.Find(b.config.SDKCandidates, runtime.GOOS)
	if err != nil {
		return nil, err
	}
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
	avds, err := b.AVDs(ctx)
	if err != nil {
		return err
	}
	index := slices.IndexFunc(avds, func(listed AVD) bool { return listed.Name == avd })
	if index < 0 {
		return fmt.Errorf("%w: %q", ErrUnknownAVD, avd)
	}

	b.mu.Lock()
	switch {
	case b.state.Connected:
		b.mu.Unlock()
		return fmt.Errorf("%w: an emulator is already running", ErrBusy)
	case b.launch.Phase == launchStarting:
		b.mu.Unlock()
		return fmt.Errorf("%w: %s is starting", ErrBusy, b.launch.AVD)
	case avds[index].Running:
		b.mu.Unlock()
		return fmt.Errorf("%w: %s is already running", ErrBusy, avd)
	}
	logPath := filepath.Join(b.config.LogDir, "emulator-"+avd+".log")
	cmd, err := bootOnFreePort(found, avd, logPath)
	if err != nil {
		b.mu.Unlock()
		return err
	}
	b.launch = Launch{AVD: avd, Phase: launchStarting}
	b.mu.Unlock()
	b.config.Logger.Info("emulator starting", "avd", avd, "pid", cmd.Process.Pid, "log", logPath)
	b.notifyViewers()
	go b.awaitExit(cmd, avd, logPath)

	return nil
}

func bootOnFreePort(found sdk.SDK, avd, logPath string) (*exec.Cmd, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	return found.Boot(avd, port, logPath)
}

// awaitExit reports a launch as failed when its emulator exits before the bridge connects to it.
func (b *Bridge) awaitExit(cmd *exec.Cmd, avd, logPath string) {
	err := cmd.Wait()
	b.mu.Lock()
	isFailed := b.launch.Phase == launchStarting && b.launch.AVD == avd
	if isFailed {
		b.launch = Launch{AVD: avd, Phase: launchFailed, Error: sdk.LastError(logPath)}
	}
	b.mu.Unlock()
	if isFailed {
		b.config.Logger.Warn("emulator exited while starting", "avd", avd, "err", err, "log", logPath)
		b.notifyViewers()
	}
}
