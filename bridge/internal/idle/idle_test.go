//go:build unix

package idle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	pb "github.com/kartollikaa/claude-android-emulator/bridge/internal/emulatorpb"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/fakeemulator"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/proc"
)

const period = time.Minute

var t0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// deadPID is the pid of a process that ran and was reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	child := exec.Command("/bin/sh", "-c", "exit 0")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	return child.Process.Pid
}

// livingPID is the pid of a process that runs until the test ends, or until the returned stop.
func livingPID(t *testing.T) (pid int, stop func()) {
	t.Helper()
	child := exec.Command("/bin/sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	stop = func() {
		child.Process.Kill()
		child.Wait()
	}
	t.Cleanup(stop)
	return child.Process.Pid, stop
}

func writeMark(t *testing.T, mark Mark, at time.Time) {
	t.Helper()
	if err := mark.Write(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(mark.path(), at, at); err != nil {
		t.Fatal(err)
	}
}

func TestLookCountsOnlyLiveFreshMarksOfTheAVD(t *testing.T) {
	dir := t.TempDir()
	self := os.Getpid()
	writeMark(t, Mark{Dir: dir, AVD: "Pixel_7_Pro", PID: self, Period: time.Hour}, t0)
	writeMark(t, Mark{Dir: dir, AVD: "Pixel_7", PID: deadPID(t), Period: time.Hour}, t0)
	writeMark(t, Mark{Dir: dir, AVD: "Pixel_7", PID: self, Period: time.Hour}, t0.Add(-staleAfter-time.Second))
	if presence := Look(dir, "Pixel_7", t0); presence.IsShown {
		t.Fatalf("Look = %+v with only another AVD's, a dead bridge's and a stale mark", presence)
	}

	older, _ := livingPID(t)
	writeMark(t, Mark{Dir: dir, AVD: "Pixel_7", PID: older, Period: 5 * time.Minute}, t0.Add(-time.Minute))
	writeMark(t, Mark{Dir: dir, AVD: "Pixel_7", PID: self, Period: 0}, t0)
	if presence := Look(dir, "Pixel_7", t0); presence != (Presence{IsShown: true, Period: 0}) {
		t.Fatalf("Look = %+v, want shown with the newest mark's period, 0", presence)
	}
}

func TestARemovedMarkIsAbsent(t *testing.T) {
	dir := t.TempDir()
	mark := Mark{Dir: dir, AVD: "Pixel_7", PID: os.Getpid(), Period: period}
	writeMark(t, mark, t0)
	if err := mark.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := mark.Remove(); err != nil {
		t.Errorf("removing a removed mark: %v", err)
	}
	if Look(dir, "Pixel_7", t0).IsShown {
		t.Error("a removed mark still counts")
	}
}

// watched starts a fake emulator of Pixel_7, whose process stopEmulator ends, and a watcher of it whose clock starts
// at t0.
func watched(t *testing.T) (fake *fakeemulator.Fake, watcher *Watcher, stopEmulator func()) {
	t.Helper()
	fake = fakeemulator.Start(t, 8, 16, "token")
	fake.AVD = "Pixel_7"
	discoveryDir := t.TempDir()
	pid, stopEmulator := livingPID(t)
	fake.WriteINI(t, discoveryDir, pid)
	watcher = &Watcher{AVD: "Pixel_7", PID: pid, Period: period, LogDir: t.TempDir(), DiscoveryDir: discoveryDir,
		Logger: quiet(), lastShown: t0}
	return fake, watcher, stopEmulator
}

func shutdowns(fake *fakeemulator.Fake) int {
	count := 0
	for _, state := range fake.VmStates() {
		if state == pb.VmRunState_SHUTDOWN {
			count++
		}
	}
	return count
}

func TestWatcherShutsTheEmulatorDownOnceThePeriodPasses(t *testing.T) {
	fake, watcher, _ := watched(t)
	ctx := context.Background()

	if watcher.check(ctx, t0.Add(period-time.Second)) || shutdowns(fake) != 0 {
		t.Fatalf("shut down before the idle period passed: %v", fake.VmStates())
	}
	if watcher.check(ctx, t0.Add(period)) {
		t.Fatal("the watch ended while the emulator still runs")
	}
	if !reflect.DeepEqual(fake.VmStates(), []pb.VmRunState_RunState{pb.VmRunState_SHUTDOWN}) {
		t.Fatalf("VM states %v, want one SHUTDOWN", fake.VmStates())
	}
	watcher.check(ctx, t0.Add(period+time.Second))
	if shutdowns(fake) != 1 {
		t.Errorf("asked to shut down %d times, want once", shutdowns(fake))
	}
}

func TestAShownEmulatorStaysUpAndTheNewestPeriodApplies(t *testing.T) {
	fake, watcher, _ := watched(t)
	ctx := context.Background()
	mark := Mark{Dir: PresenceDir(watcher.LogDir), AVD: "Pixel_7", PID: os.Getpid(), Period: 2 * period}

	shown := t0.Add(10 * period)
	writeMark(t, mark, shown)
	watcher.check(ctx, shown)
	if shutdowns(fake) != 0 {
		t.Fatal("shut down an emulator a bridge shows")
	}
	if watcher.Period != 2*period {
		t.Errorf("period %v, want the mark's %v", watcher.Period, 2*period)
	}

	mark.Remove()
	watcher.check(ctx, shown.Add(2*period-time.Second))
	if shutdowns(fake) != 0 {
		t.Fatal("the idle clock did not restart when the emulator was last shown")
	}
	watcher.check(ctx, shown.Add(2*period))
	if shutdowns(fake) != 1 {
		t.Errorf("not shut down %v after the last bridge left", 2*period)
	}
}

func TestAPeriodOfZeroNeverShutsDown(t *testing.T) {
	fake, watcher, _ := watched(t)
	watcher.Period = 0

	watcher.check(context.Background(), t0.Add(1000*time.Hour))

	if shutdowns(fake) != 0 {
		t.Error("shut down with the idle shutdown off")
	}
}

func TestTheWatchEndsWhenTheEmulatorIsGone(t *testing.T) {
	fake, watcher, stopEmulator := watched(t)
	stopEmulator()

	if !watcher.check(context.Background(), t0.Add(2*period)) {
		t.Error("the watch went on with the emulator gone")
	}
	if shutdowns(fake) != 0 {
		t.Error("asked a gone emulator to shut down")
	}
}

func TestOneWatcherPerEmulator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.lock")
	release, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("a second lock: %v, want ErrLocked", err)
	}
	release()
	again, err := Lock(path)
	if err != nil {
		t.Fatalf("the lock was not given back: %v", err)
	}
	again()
}

func TestASecondWatcherLeavesAtOnce(t *testing.T) {
	_, watcher, _ := watched(t)
	release, err := Lock(LockPath(watcher.LogDir, watcher.AVD, watcher.PID))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := watcher.Run(ctx); err != nil || ctx.Err() != nil {
		t.Fatalf("Run = %v, ctx %v; want it to return at once", err, ctx.Err())
	}
}

func TestRunShutsDownAnIdleEmulatorAndEndsWithIt(t *testing.T) {
	fake, watcher, stopEmulator := watched(t)
	pid := watcher.PID
	watcher.Period = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx) }()

	for shutdowns(fake) == 0 {
		if ctx.Err() != nil {
			t.Fatal("the idle emulator was never shut down")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopEmulator()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("the watcher outlived its emulator")
	}
	if _, err := os.Stat(LockPath(watcher.LogDir, watcher.AVD, pid)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the lock file stayed behind: %v", err)
	}
}

func TestAnEmulatorShownAgainAfterItsShutdownWasAskedWaitsAFullPeriod(t *testing.T) {
	fake, watcher, _ := watched(t)
	ctx := context.Background()
	watcher.Period = 10 * time.Minute
	asked := t0.Add(watcher.Period)
	watcher.check(ctx, asked)
	if shutdowns(fake) != 1 {
		t.Fatal("the idle emulator was not asked to shut down")
	}

	mark := Mark{Dir: PresenceDir(watcher.LogDir), AVD: "Pixel_7", PID: os.Getpid(), Period: watcher.Period}
	shown := asked.Add(time.Second)
	writeMark(t, mark, shown)
	watcher.check(ctx, shown)
	mark.Remove()
	watcher.check(ctx, shown.Add(shutdownGrace+time.Second))

	time.Sleep(300 * time.Millisecond)
	if !proc.Alive(watcher.PID) {
		t.Fatal("the emulator was stopped the hard way before a full idle period passed since it was last shown")
	}
}

func TestTheWatcherFollowsThePidTheDiscoveryFileNames(t *testing.T) {
	_, watcher, stopEmulator := watched(t)
	emulator := watcher.PID
	watcher.PID = deadPID(t)

	if watcher.check(context.Background(), t0) {
		t.Fatal("the watch ended when the launcher's pid did, with the discovery file naming a running emulator")
	}
	if watcher.PID != emulator {
		t.Errorf("following pid %d, want the discovery file's %d", watcher.PID, emulator)
	}
	stopEmulator()
	if !watcher.check(context.Background(), t0) {
		t.Error("the watch went on with the emulator the discovery file names gone")
	}
}
