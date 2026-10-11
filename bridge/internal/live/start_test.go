//go:build unix

package live

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	pb "github.com/kartollikaa/claude-android-emulator/bridge/internal/emulatorpb"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/fakeemulator"
)

// The fake `emulator` lists two AVDs among a log line and records each boot's pid and arguments.
const listingScript = `
d=$(dirname "$0")
if [ "$1" = "-list-avds" ]; then
  printf '%s\n' "INFO    | Android emulator version 36.6.11.0" Pixel_7 Second
  exit 0
fi
echo "$$ $*" >> "$d/launched"
`

const stayingUp = listingScript + `
echo "emulator booting"
exec sleep 30
`

func fakeSDK(t *testing.T, script string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "emulator"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "emulator", "emulator"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, launched := range launches(root) {
			syscall.Kill(-launched.pid, syscall.SIGKILL)
		}
	})

	return root
}

type launched struct {
	pid  int
	args []string
}

func launches(sdkRoot string) []launched {
	data, _ := os.ReadFile(filepath.Join(sdkRoot, "emulator", "launched"))
	var found []launched
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		found = append(found, launched{pid: pid, args: fields[1:]})
	}
	return found
}

func withSDK(root string) func(*Config) {
	return func(config *Config) { config.SDKCandidates = []string{root} }
}

func send(t *testing.T, h *harness, method, path, contentType, origin, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(method, h.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(data)
}

func start(t *testing.T, h *harness, avd string) int {
	t.Helper()
	status, _ := send(t, h, http.MethodPost, "/emulators/start", "application/json", "", `{"avd":"`+avd+`"}`)
	return status
}

func TestAVDsListTheSDKsVirtualDevices(t *testing.T) {
	root := fakeSDK(t, listingScript)
	avdHome := t.TempDir()
	pixel := filepath.Join(t.TempDir(), "elsewhere", "Pixel_7.avd")
	if err := os.MkdirAll(pixel, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pixel, "config.ini"), "avd.ini.displayname=Pixel 7\r\ntarget=android-37.1\r\n")
	writeFile(t, filepath.Join(avdHome, "Pixel_7.ini"), "avd.ini.encoding=UTF-8\npath="+pixel+"\n")
	h := startBridge(t, withSDK(root), func(config *Config) { config.AVDHome = avdHome })
	writeFile(t, ownDiscoveryFile(h), "avd.id=Second\nport.serial=5558\n")

	status, body := send(t, h, http.MethodGet, "/avds", "", "", "")

	if status != http.StatusOK {
		t.Fatalf("/avds answered %d: %s", status, body)
	}
	var answer struct{ AVDs []map[string]any }
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"name": "Pixel_7", "displayName": "Pixel 7", "api": "37.1", "running": false},
		{"name": "Second", "displayName": "Second", "api": "", "running": true},
	}
	if !reflect.DeepEqual(answer.AVDs, want) {
		t.Fatalf("avds = %v, want %v", answer.AVDs, want)
	}
}

func TestWithoutAnSDK(t *testing.T) {
	empty := t.TempDir()
	missing := filepath.Join(t.TempDir(), "absent")
	h := startBridge(t, func(config *Config) { config.SDKCandidates = []string{empty, missing} })

	status, body := send(t, h, http.MethodGet, "/avds", "", "", "")

	var answer struct{ Error string }
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatalf("/avds body %q is not JSON: %v", body, err)
	}
	if status != http.StatusServiceUnavailable || !strings.Contains(answer.Error, empty) || !strings.Contains(answer.Error, missing) {
		t.Errorf("/avds answered %d %q, want 503 naming %s and %s", status, answer.Error, empty, missing)
	}
	if status := start(t, h, "Pixel_7"); status != http.StatusServiceUnavailable {
		t.Errorf("start answered %d, want 503", status)
	}
}

func TestStartLaunchesTheEmulator(t *testing.T) {
	root := fakeSDK(t, stayingUp)
	h := startBridge(t, withSDK(root))

	if status := start(t, h, "Pixel_7"); status != http.StatusAccepted {
		t.Fatalf("start answered %d, want 202", status)
	}

	eventually(t, func() bool { return len(launches(root)) == 1 }, "the emulator being launched")
	args := launches(root)[0].args
	if len(args) != 8 {
		t.Fatalf("emulator ran with %v", args)
	}
	want := []string{"-avd", "Pixel_7", "-qt-hide-window", "-gpu", "host", "-grpc", args[6], "-grpc-use-token"}
	if !slices.Equal(args, want) {
		t.Fatalf("emulator ran with %v, want %v", args, want)
	}
	if port, err := strconv.Atoi(args[6]); err != nil || port <= 0 {
		t.Fatalf("gRPC port %q is not a port", args[6])
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+args[6])
	if err != nil {
		t.Fatalf("gRPC port %s was not free: %v", args[6], err)
	}
	listener.Close()
	if launch := h.status(t).Launch; launch != (Launch{AVD: "Pixel_7", Phase: "starting"}) {
		t.Fatalf("launch = %+v, want Pixel_7 starting", launch)
	}
}

func TestLaunchedEmulatorIsDetached(t *testing.T) {
	root := fakeSDK(t, stayingUp)
	logDir := t.TempDir()
	h := startBridge(t, withSDK(root), func(config *Config) { config.LogDir = logDir })

	if status := start(t, h, "Pixel_7"); status != http.StatusAccepted {
		t.Fatalf("start answered %d, want 202", status)
	}

	eventually(t, func() bool { return len(launches(root)) == 1 }, "the emulator being launched")
	pid := launches(root)[0].pid
	session, err := unix.Getsid(pid)
	if err != nil {
		t.Fatal(err)
	}
	ownSession, _ := unix.Getsid(0)
	if session == ownSession || session != pid {
		t.Errorf("emulator %d runs in session %d, the bridge in %d; want a session of its own", pid, session, ownSession)
	}
	logPath := filepath.Join(logDir, "emulator-Pixel_7.log")
	eventually(t, func() bool {
		data, _ := os.ReadFile(logPath)
		return strings.Contains(string(data), "emulator booting")
	}, "the emulator's output landing in "+logPath)
	wantOutput, _ := filepath.EvalSymlinks(logPath)
	if kind, name := standardOutput(t, pid); kind != "REG" || name != wantOutput {
		t.Errorf("the emulator's standard output is %s %s, want the regular file %s", kind, name, logPath)
	}
	h.stop()
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("the emulator did not outlive the bridge: %v", err)
	}
	if defaultDir := New(Config{}).config.LogDir; defaultDir != filepath.Join(os.TempDir(), "claude-android-emulator") {
		t.Errorf("default log dir = %s, want $TMPDIR/claude-android-emulator", defaultDir)
	}
}

func TestLaunchedEmulatorIsOutsideTheBridgesTree(t *testing.T) {
	root := fakeSDK(t, stayingUp)
	h := startBridge(t, withSDK(root))

	if status := start(t, h, "Pixel_7"); status != http.StatusAccepted {
		t.Fatalf("start answered %d, want 202", status)
	}

	eventually(t, func() bool { return len(launches(root)) == 1 }, "the emulator being launched")
	pid := launches(root)[0].pid
	for ancestor := parentOf(t, pid); ancestor > 1; ancestor = parentOf(t, ancestor) {
		if ancestor == os.Getpid() {
			t.Fatalf("emulator %d descends from the bridge %d, so killing the bridge's tree kills it", pid, os.Getpid())
		}
	}
}

func TestStartRefusesWhatItCannotLaunch(t *testing.T) {
	root := fakeSDK(t, stayingUp)
	h := startBridge(t, withSDK(root))
	writeFile(t, ownDiscoveryFile(h), "avd.id=Second\nport.serial=5560\n")

	for _, step := range []struct {
		name, contentType, origin, body string
		want                            int
	}{
		{"unknown AVD", "application/json", "", `{"avd":"Nope"}`, http.StatusNotFound},
		{"a flag for an AVD", "application/json", "", `{"avd":"-wipe-data"}`, http.StatusNotFound},
		{"no AVD", "application/json", "", `{}`, http.StatusBadRequest},
		{"not JSON", "text/plain", "", `{"avd":"Pixel_7"}`, http.StatusUnsupportedMediaType},
		{"foreign origin", "application/json", "https://evil.example", `{"avd":"Pixel_7"}`, http.StatusForbidden},
		{"AVD already running", "application/json", "", `{"avd":"Second"}`, http.StatusConflict},
	} {
		if status, body := send(t, h, http.MethodPost, "/emulators/start", step.contentType, step.origin, step.body); status != step.want {
			t.Errorf("%s answered %d %q, want %d", step.name, status, body, step.want)
		}
		if launch := h.status(t).Launch; launch != (Launch{}) {
			t.Fatalf("%s left launch %+v, want none", step.name, launch)
		}
	}
	if status := start(t, h, "Pixel_7"); status != http.StatusAccepted {
		t.Fatalf("a valid start answered %d, want 202", status)
	}
	if status := start(t, h, "Pixel_7"); status != http.StatusConflict {
		t.Errorf("a start while one is starting answered %d, want 409", status)
	}
	eventually(t, func() bool { return len(launches(root)) == 1 }, "the valid start launching")
	time.Sleep(100 * time.Millisecond)
	if count := len(launches(root)); count != 1 {
		t.Errorf("%d emulators launched, want only the valid start", count)
	}

	connectedRoot := fakeSDK(t, stayingUp)
	connected := startBridge(t, withSDK(connectedRoot))
	connected.connected(t, testContext(t), "")
	if status := start(t, connected, "Pixel_7"); status != http.StatusConflict {
		t.Errorf("a start while an emulator is connected answered %d, want 409", status)
	}
	time.Sleep(100 * time.Millisecond)
	if count := len(launches(connectedRoot)); count != 0 {
		t.Errorf("%d emulators launched while one was connected", count)
	}
}

// Each boot fails with the next log: ERROR then FATAL, two ERRORs, then neither.
const failingThrice = listingScript + `
attempt=$(cat "$d/attempts" 2>/dev/null || echo 0)
echo $((attempt + 1)) > "$d/attempts"
case $attempt in
0)
  echo "INFO    | Android emulator version 36.6.11.0"
  echo "ERROR   | first problem"
  echo "FATAL   | x86_64 emulation currently requires hardware acceleration"
  echo "INFO    | shutting down"
  ;;
1)
  echo "ERROR   | one"
  echo "ERROR   | two"
  echo "INFO    | shutting down"
  ;;
*)
  echo "INFO    | starting again"
  echo "plain last line"
  echo
  ;;
esac
exit 1
`

func TestFailedLaunchIsReported(t *testing.T) {
	root := fakeSDK(t, failingThrice)
	h := startBridge(t, withSDK(root))

	for _, wantError := range []string{
		"FATAL   | x86_64 emulation currently requires hardware acceleration",
		"ERROR   | two",
		"plain last line",
	} {
		if status := start(t, h, "Pixel_7"); status != http.StatusAccepted {
			t.Fatalf("start answered %d, want 202", status)
		}
		want := Launch{AVD: "Pixel_7", Phase: "failed", Error: wantError}
		eventuallyWithin(t, 2*time.Second, func() bool { return h.status(t).Launch == want }, "the launch reported as failed with "+wantError)
	}
}

func TestBootProgress(t *testing.T) {
	root := fakeSDK(t, stayingUp)
	h := startBridge(t, withSDK(root))
	ctx := testContext(t)
	if status := start(t, h, "Pixel_7"); status != http.StatusAccepted {
		t.Fatalf("start answered %d, want 202", status)
	}

	fake := fakeemulator.Start(t, deviceWidth, deviceHeight, "")
	fake.AVD = "Pixel_7"
	fake.SetBooted(false)
	fake.WriteINI(t, h.dir, 1)
	fake.NextStream(t, ctx)
	state := h.waitFor(t, func(state State) bool { return state.Connected })
	time.Sleep(100 * time.Millisecond)

	if state := h.status(t); state.Booted {
		t.Fatal("booted while the emulator still boots")
	}
	if state.Launch != (Launch{}) || state.AVD != "Pixel_7" {
		t.Fatalf("connected to %q with launch %+v, want Pixel_7 and the launch cleared", state.AVD, state.Launch)
	}
	fake.SetBooted(true)
	eventuallyWithin(t, 2*time.Second, func() bool { return h.status(t).Booted }, "booted once the emulator reports it")
}

// standardOutput reads what fd 1 of pid is, as lsof names it: its type (REG, PIPE) and path.
func standardOutput(t *testing.T, pid int) (kind, name string) {
	t.Helper()
	if runtime.GOOS == "linux" {
		name, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/1", pid))
		if err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(name); err == nil && info.Mode().IsRegular() {
			return "REG", name
		}
		return "PIPE", name
	}
	output, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "1", "-F", "tn").Output()
	if err != nil {
		t.Fatalf("lsof: %v", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "t") {
			kind = line[1:]
		}
		if strings.HasPrefix(line, "n") {
			name = line[1:]
		}
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err == nil {
		name = resolved
	}
	return kind, name
}

func parentOf(t *testing.T, pid int) int {
	t.Helper()
	var field string
	if runtime.GOOS == "linux" {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			t.Fatal(err)
		}
		afterName := string(stat[bytes.LastIndexByte(stat, ')')+1:])
		field = strings.Fields(afterName)[1]
	} else {
		output, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			t.Fatalf("ps: %v", err)
		}
		field = strings.TrimSpace(string(output))
	}
	parent, err := strconv.Atoi(field)
	if err != nil {
		t.Fatalf("parent of %d: %q", pid, field)
	}
	return parent
}

func ownDiscoveryFile(h *harness) string {
	return filepath.Join(h.dir, "pid_"+strconv.Itoa(os.Getpid())+".ini")
}

func TestStuckLaunchIsStopped(t *testing.T) {
	root := fakeSDK(t, stayingUp)
	logDir := t.TempDir()
	h := startBridge(t, withSDK(root), func(config *Config) {
		config.LaunchTimeout = 300 * time.Millisecond
		config.LogDir = logDir
	})

	if status := start(t, h, "Pixel_7"); status != http.StatusAccepted {
		t.Fatalf("start answered %d, want 202", status)
	}

	eventuallyWithin(t, 2*time.Second, func() bool { return h.status(t).Launch.Phase == "failed" }, "the stuck launch reported as failed")
	launch := h.status(t).Launch
	if !strings.Contains(launch.Error, "did not come up") || !strings.Contains(launch.Error, filepath.Join(logDir, "emulator-Pixel_7.log")) {
		t.Errorf("error = %q, want it to say the emulator did not come up and name its log", launch.Error)
	}
	pid := launches(root)[0].pid
	eventuallyWithin(t, 2*time.Second, func() bool { return syscall.Kill(pid, 0) != nil }, "the stuck emulator being stopped")
	if status := start(t, h, "Pixel_7"); status != http.StatusAccepted {
		t.Errorf("a start after the stuck launch answered %d, want 202", status)
	}
}

func TestShutdownAsksTheEmulatorToShutDown(t *testing.T) {
	h := startBridge(t)
	if status, _ := send(t, h, http.MethodPost, "/shutdown", "application/json", "", `{}`); status != http.StatusServiceUnavailable {
		t.Errorf("shutdown with no emulator answered %d, want 503", status)
	}
	fake, _ := h.connected(t, testContext(t), "")

	for _, step := range []struct {
		name, contentType, origin string
		want                      int
	}{
		{"not JSON", "text/plain", "", http.StatusUnsupportedMediaType},
		{"foreign origin", "application/json", "https://evil.example", http.StatusForbidden},
	} {
		if status, _ := send(t, h, http.MethodPost, "/shutdown", step.contentType, step.origin, `{}`); status != step.want {
			t.Errorf("%s answered %d, want %d", step.name, status, step.want)
		}
	}
	if states := fake.VmStates(); len(states) != 0 {
		t.Fatalf("refused requests reached the emulator: %v", states)
	}
	if status, body := send(t, h, http.MethodPost, "/shutdown", "application/json", "", `{}`); status != http.StatusAccepted {
		t.Fatalf("shutdown answered %d %q, want 202", status, body)
	}

	if states := fake.VmStates(); !slices.Equal(states, []pb.VmRunState_RunState{pb.VmRunState_SHUTDOWN}) {
		t.Fatalf("VM states = %v, want one SHUTDOWN", states)
	}
	h.waitFor(t, func(state State) bool { return !state.Connected })
}

func TestDeviceSizeIgnoresTheRotationAtConnect(t *testing.T) {
	h := startBridge(t)
	fake := fakeemulator.Start(t, deviceWidth, deviceHeight, "")
	fake.Rotated = true
	fake.WriteINI(t, h.dir, 1)
	fake.NextStream(t, testContext(t))

	state := h.waitFor(t, func(state State) bool { return state.Connected })

	if state.Device != deviceSize() {
		t.Fatalf("device = %+v with the emulator turned landscape, want the portrait panel %+v", state.Device, deviceSize())
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}
