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
	"syscall"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/discovery"
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
	verbose := flag.Bool("v", false, "log debug detail")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	config := live.Config{DiscoveryDir: *discoveryDir, StreamWidth: *streamWidth, CapturesDir: *capturesDir, ADB: *adbPath, Logger: logger}
	if *sdkDir != "" {
		config.SDKCandidates = []string{*sdkDir}
	}
	if err := run(*port, config); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

const cleanupTimeout = 5 * time.Second

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
