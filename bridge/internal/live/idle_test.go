//go:build unix

package live

import (
	"context"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	pb "github.com/kartollikaa/claude-android-emulator/bridge/internal/emulatorpb"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/fakeemulator"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/idle"
)

func TestAShownEmulatorKeepsAPresenceMarkUntilItGoes(t *testing.T) {
	h := startBridge(t, func(config *Config) { config.IdleShutdown = 7 * time.Minute })
	ctx := testContext(t)
	presenceDir := idle.PresenceDir(h.bridge.config.LogDir)
	fake, _ := h.connectedAs(t, ctx, "Pixel_7")

	eventually(t, func() bool { return idle.Look(presenceDir, "Pixel_7", time.Now()).IsShown }, "a presence mark")
	if presence := idle.Look(presenceDir, "Pixel_7", time.Now()); presence.Period != 7*time.Minute {
		t.Errorf("the mark carries %v, want the bridge's idle period", presence.Period)
	}

	fake.Stop()
	os.Remove(h.dir + "/pid_1.ini")
	h.waitFor(t, func(state State) bool { return !state.Connected })
	if idle.Look(presenceDir, "Pixel_7", time.Now()).IsShown {
		t.Error("the mark outlived the emulator going away")
	}
}

func TestAStoppedBridgeLeavesNoPresenceMark(t *testing.T) {
	h := startBridge(t)
	ctx := testContext(t)
	presenceDir := idle.PresenceDir(h.bridge.config.LogDir)
	h.connectedAs(t, ctx, "Pixel_7")
	eventually(t, func() bool { return idle.Look(presenceDir, "Pixel_7", time.Now()).IsShown }, "a presence mark")

	h.stop()

	eventually(t, func() bool {
		entries, _ := os.ReadDir(presenceDir)
		return len(entries) == 0
	}, "the mark being removed")
}

type watchRequests struct {
	mu   sync.Mutex
	avds []string
	pids []int
}

func (w *watchRequests) record(avd string, pid int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.avds = append(w.avds, avd)
	w.pids = append(w.pids, pid)
	return nil
}

func (w *watchRequests) seen() ([]string, []int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.avds...), append([]int(nil), w.pids...)
}

func TestABootedEmulatorGetsAnIdleWatcher(t *testing.T) {
	root := fakeSDK(t, stayingUp)
	var watches watchRequests
	h := startBridge(t, withSDK(root), func(config *Config) {
		config.IdleShutdown = time.Minute
		config.WatchIdle = watches.record
	})

	if status := start(t, h, "Pixel_7"); status != http.StatusAccepted {
		t.Fatalf("start answered %d, want 202", status)
	}

	eventually(t, func() bool { return len(launches(root)) == 1 }, "the emulator being launched")
	avds, pids := watches.seen()
	if len(avds) != 1 || avds[0] != "Pixel_7" || pids[0] != launches(root)[0].pid {
		t.Fatalf("watched %v %v, want Pixel_7 with the emulator's pid %d", avds, pids, launches(root)[0].pid)
	}
}

func TestNoIdleWatcherWhenTheIdleShutdownIsOff(t *testing.T) {
	root := fakeSDK(t, stayingUp)
	var watches watchRequests
	h := startBridge(t, withSDK(root), func(config *Config) { config.WatchIdle = watches.record })

	start(t, h, "Pixel_7")

	eventually(t, func() bool { return len(launches(root)) == 1 }, "the emulator being launched")
	if avds, _ := watches.seen(); len(avds) != 0 {
		t.Errorf("watched %v with the idle shutdown off", avds)
	}
}

// connectedAs connects a fake emulator whose discovery file names avd.
func (h *harness) connectedAs(t *testing.T, ctx context.Context, avd string) (*fakeemulator.Fake, chan<- *pb.Image) {
	t.Helper()
	fake := fakeemulator.Start(t, deviceWidth, deviceHeight, "")
	fake.AVD = avd
	fake.WriteINI(t, h.dir, 1)
	frames := fake.NextStream(t, ctx)
	h.waitFor(t, func(state State) bool { return state.Connected })
	return fake, frames
}
