// Command android-bridge serves the running emulator's or a connected phone's screen and input on http://127.0.0.1:<port>.
// It writes {"liveView":"<url>"} to stdout once it listens; logs go to stderr.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/discovery"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/idle"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/live"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/sdk"
)

func main() {
	sdk.RunLauncherIfAsked()
	port := flag.Int("port", 7357, "port of the live view on 127.0.0.1; 0 picks a free one")
	streamWidth := flag.Int("stream-width", 540, "the streamed frames' short side in pixels, their width in portrait")
	discoveryDir := flag.String("discovery-dir", discovery.DefaultDir(), "where running emulators write pid_<pid>.ini")
	sdkDir := flag.String("sdk", "", "Android SDK folder (default: ANDROID_HOME, ANDROID_SDK_ROOT, then the platform's default)")
	capturesDir := flag.String("captures-dir", "", "where screenshots and screen recordings are saved (default: the Desktop)")
	adbPath := flag.String("adb", "", "the adb binary (default: the SDK's platform-tools, then PATH)")
	idleShutdown := flag.Duration("idle-shutdown", 15*time.Minute, "shut down an emulator the bridge boots once no bridge has shown it for this long; 0 never")
	logDir := flag.String("log-dir", filepath.Join(os.TempDir(), "claude-android-emulator"), "where emulator logs, presence marks and watcher locks go")
	watchAVD := flag.String("watch-emulator", "", "run as the idle watcher of this AVD's emulator instead of serving the live view")
	emulatorPID := flag.Int("emulator-pid", 0, "the watched emulator's pid, with -watch-emulator")
	verbose := flag.Bool("v", false, "log debug detail")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if *watchAVD != "" {
		watcher := idle.Watcher{AVD: *watchAVD, PID: *emulatorPID, Period: *idleShutdown, LogDir: *logDir,
			DiscoveryDir: *discoveryDir, Logger: logger}
		if err := watcher.Run(context.Background()); err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}
		return
	}

	config := live.Config{DiscoveryDir: *discoveryDir, StreamWidth: *streamWidth, CapturesDir: *capturesDir, ADB: *adbPath,
		Logger: logger, LogDir: *logDir, IdleShutdown: *idleShutdown}
	config.WatchIdle = func(avd string, pid int) error {
		_, err := startWatcher(config, avd, pid)
		return err
	}
	if *sdkDir != "" {
		config.SDKCandidates = []string{*sdkDir}
	}
	if err := run(*port, config); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

const cleanupTimeout = 5 * time.Second

// startWatcher runs this binary as the idle watcher of the emulator for avd, outside the bridge's process tree so it
// outlives the session; it returns the watcher's pid.
func startWatcher(config live.Config, avd string, pid int) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	watcher, err := sdk.Detach([]string{self, "-watch-emulator", avd, "-emulator-pid", strconv.Itoa(pid),
		"-idle-shutdown", config.IdleShutdown.String(), "-log-dir", config.LogDir, "-discovery-dir", config.DiscoveryDir},
		idle.LogPath(config.LogDir, avd))
	if err == nil {
		config.Logger.Info("watching the emulator for idleness", "avd", avd, "watcher", watcher, "idle", config.IdleShutdown)
	}

	return watcher, err
}

func run(port int, config live.Config) error {
	listener, err := live.Listen(port)
	if err != nil {
		return fmt.Errorf("cannot open port %d on 127.0.0.1 (is another bridge running?): %w", port, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go stopWithParent(ctx, stop)

	bridge := live.New(config)
	ran := make(chan struct{})
	go func() {
		bridge.Run(ctx)
		close(ran)
	}()
	defer stopAndWait(stop, ran, cleanupTimeout)

	server := &http.Server{Handler: bridge.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	url := "http://" + listener.Addr().String()
	config.Logger.Info("live view ready", "url", url)
	if err := json.NewEncoder(os.Stdout).Encode(announcement{LiveView: url}); err != nil {
		return err
	}
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// stopAndWait stops the bridge and waits, up to limit, for its run to end: a phone's stream removes its adb forward as
// it ends, which the bridge lets happen before it exits, whether a signal or a failing server ends it.
func stopAndWait(stop context.CancelFunc, ran <-chan struct{}, limit time.Duration) {
	stop()
	select {
	case <-ran:
	case <-time.After(limit):
	}
}

// announcement is the one line the bridge writes to stdout, for the mod that started it.
type announcement struct {
	LiveView string `json:"liveView"`
}

// stopWithParent stops the bridge once its parent is gone; macOS and Linux then give it a new parent, Windows never does.
func stopWithParent(ctx context.Context, stop context.CancelFunc) {
	parent := os.Getppid()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
		if os.Getppid() != parent {
			stop()
			return
		}
	}
}
