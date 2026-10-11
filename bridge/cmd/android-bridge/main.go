// Command android-bridge streams the running Android emulator to a live view on
// http://127.0.0.1:<port> and forwards touches and keys back to it.
package main

import (
	"context"
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
)

func main() {
	port := flag.Int("port", 7357, "port of the live view on 127.0.0.1")
	streamWidth := flag.Int("stream-width", 540, "width in pixels the emulator streams frames at")
	discoveryDir := flag.String("discovery-dir", discovery.DefaultDir(), "where running emulators write pid_<pid>.ini")
	verbose := flag.Bool("v", false, "log debug detail")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(*port, live.Config{DiscoveryDir: *discoveryDir, StreamWidth: *streamWidth, Logger: logger}); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

func run(port int, config live.Config) error {
	listener, err := live.Listen(port)
	if err != nil {
		return fmt.Errorf("cannot open port %d on 127.0.0.1 (is another bridge running?): %w", port, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bridge := live.New(config)
	go bridge.Run(ctx)

	server := &http.Server{Handler: bridge.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	config.Logger.Info("live view ready", "url", "http://"+listener.Addr().String())
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}
