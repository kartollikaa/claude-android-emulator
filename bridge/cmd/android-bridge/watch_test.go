//go:build unix

package main

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	pb "github.com/kartollikaa/claude-android-emulator/bridge/internal/emulatorpb"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/fakeemulator"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/live"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/proc"
)

func TestTheIdleWatcherRunsDetachedAndShutsTheEmulatorDown(t *testing.T) {
	t.Setenv(beTheBridge, "1")
	fake := fakeemulator.Start(t, 8, 16, "token")
	fake.AVD = "Pixel_7"
	config := live.Config{DiscoveryDir: t.TempDir(), LogDir: t.TempDir(), IdleShutdown: 300 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	fake.WriteINI(t, config.DiscoveryDir, os.Getpid())
	emulator := exec.Command("/bin/sleep", "60")
	if err := emulator.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		emulator.Process.Kill()
		emulator.Wait()
	})

	watcher, err := startWatcher(config, "Pixel_7", emulator.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Kill(watcher, unix.SIGKILL) })

	deadline := time.Now().Add(10 * time.Second)
	for !slices.Contains(fake.VmStates(), pb.VmRunState_SHUTDOWN) {
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(config.LogDir + "/watch-Pixel_7.log")
			t.Fatalf("no SHUTDOWN reached the emulator; the watcher's log:\n%s", log)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if session, _ := unix.Getsid(watcher); session != watcher {
		t.Errorf("the watcher runs in session %d, want one of its own (%d)", session, watcher)
	}
	emulator.Process.Kill()
	emulator.Wait()
	for proc.Alive(watcher) {
		if time.Now().After(deadline) {
			t.Fatal("the watcher outlived its emulator")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
