package idle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/discovery"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/emulator"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/proc"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/sdk"
)

const (
	maxPoll = 5 * time.Second
	// How long a powered-off emulator may take to exit before the watcher stops it the hard way.
	shutdownGrace = 2 * time.Minute
	dialTimeout   = 10 * time.Second
)

// LockPath is the lock a watcher of the emulator with pid holds while it runs.
func LockPath(logDir, avd string, pid int) string {
	return filepath.Join(logDir, "watch-"+avd+"-"+strconv.Itoa(pid)+".lock")
}

// LogPath is where a watcher of avd logs.
func LogPath(logDir, avd string) string { return filepath.Join(logDir, "watch-"+avd+".log") }

// Watcher shuts the emulator for AVD down once no bridge has shown it for Period. PID is the process Start launched,
// until the AVD's discovery file names the emulator's own.
type Watcher struct {
	AVD          string
	PID          int
	Period       time.Duration
	LogDir       string
	DiscoveryDir string
	Logger       *slog.Logger
	Now          func() time.Time

	lastShown  time.Time
	askedAt    time.Time
	terminated bool
}

// Run watches until the emulator is gone or ctx ends; it returns at once when another watcher has the emulator.
func (w *Watcher) Run(ctx context.Context) error {
	if w.Now == nil {
		w.Now = time.Now
	}
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
	release, err := Lock(LockPath(w.LogDir, w.AVD, w.PID))
	if errors.Is(err, ErrLocked) {
		w.Logger.Info("another watcher has this emulator", "avd", w.AVD, "pid", w.PID)
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	w.lastShown = w.Now()
	w.Logger.Info("watching", "avd", w.AVD, "pid", w.PID, "idle", w.Period)
	for !w.check(ctx, w.Now()) {
		timer := time.NewTimer(pollInterval(w.Period))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil
		}
	}

	return nil
}

func pollInterval(period time.Duration) time.Duration {
	if period <= 0 {
		return maxPoll
	}

	return min(maxPoll, max(period/4, 10*time.Millisecond))
}

// check takes one look at now and reports whether the watch is over, the emulator being gone.
func (w *Watcher) check(ctx context.Context, now time.Time) bool {
	w.follow()
	if !proc.Alive(w.PID) {
		w.Logger.Info("the emulator is gone", "avd", w.AVD)
		return true
	}
	if presence := Look(PresenceDir(w.LogDir), w.AVD, now); presence.IsShown {
		w.lastShown = now
		w.askedAt, w.terminated = time.Time{}, false
		if presence.Period != w.Period {
			w.Logger.Info("idle period changed", "avd", w.AVD, "idle", presence.Period)
			w.Period = presence.Period
		}
		return false
	}
	switch {
	case !w.askedAt.IsZero():
		if !w.terminated && now.Sub(w.askedAt) >= shutdownGrace {
			w.Logger.Warn("the emulator did not power off; stopping it", "avd", w.AVD)
			sdk.Stop(w.PID)
			w.terminated = true
		}
	case w.Period > 0 && now.Sub(w.lastShown) >= w.Period:
		if err := w.shutdown(ctx); err != nil {
			w.Logger.Warn("cannot shut the idle emulator down yet", "avd", w.AVD, "err", err)
			return false
		}
		w.Logger.Info("shut down an idle emulator", "avd", w.AVD, "idle", now.Sub(w.lastShown))
		w.askedAt = now
	}

	return false
}

// follow switches to the pid the AVD's discovery file names once it names a running one.
func (w *Watcher) follow() {
	found, err := discovery.Find(w.DiscoveryDir)
	if err != nil {
		return
	}
	for _, candidate := range found {
		if candidate.AVD == w.AVD && candidate.PID != w.PID && proc.Alive(candidate.PID) {
			w.Logger.Info("following the emulator's own pid", "avd", w.AVD, "pid", candidate.PID, "launched", w.PID)
			w.PID = candidate.PID
			return
		}
	}
}

// shutdown asks the emulator, found by its discovery file, to power off as Shut down in the live view does.
func (w *Watcher) shutdown(ctx context.Context) error {
	found, err := discovery.Find(w.DiscoveryDir)
	if err != nil {
		return err
	}
	for _, candidate := range found {
		if candidate.AVD != w.AVD || !proc.Alive(candidate.PID) {
			continue
		}
		dialing, cancel := context.WithTimeout(ctx, dialTimeout)
		conn, err := emulator.Dial(dialing, candidate, w.Logger)
		if err != nil {
			cancel()
			return err
		}
		err = conn.Shutdown(dialing)
		conn.Close()
		cancel()

		return err
	}

	return fmt.Errorf("no running emulator of %s serves gRPC", w.AVD)
}
