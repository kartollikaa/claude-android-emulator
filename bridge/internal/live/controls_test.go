//go:build unix

package live

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/device"
	pb "github.com/kartollikaa/claude-android-emulator/bridge/internal/emulatorpb"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/fakeemulator"
)

// The fake adb logs calls, keeps device files in device/, records until SIGINT and runs other shell commands through sh with device-bin/ first
// (ADB_SERIAL naming the device); offline, broken, limit, pull-time and launchable files steer it. For a phone it lists devices, forwards to
// the port in scrcpy-port (unless forward-broken), holds the scrcpy server's shell open, answers screencap with screen.png and dumpsys window with window.
const fakeADB = `#!/bin/sh
d=$(dirname "$0")
[ "$1" = devices ] && { cat "$d/devices" 2>/dev/null; exit 0; }
echo "$*" >> "$d/calls"
[ -f "$d/offline" ] && { echo "adb: device offline" >&2; exit 1; }
export ADB_SERIAL="$2"
shift 2
case "$1" in
shell)
  case "$2" in
  *"exec screenrecord "*)
    name=$(basename "${2##* }")
    echo $$ >> "$d/recorders"
    echo $$
    [ -f "$d/broken" ] && { cat "$d/broken" >&2; exit 1; }
    finish() { printf 'MP4 %s' "$name" > "$d/device/$name"; exit 0; }
    trap finish INT
    if [ -f "$d/limit" ]; then sleep "$(cat "$d/limit")"; finish; fi
    while :; do sleep 0.05; done ;;
  *app_process*) echo $$ >> "$d/servers"; exec sleep 600 ;;
  kill*) sh -c "$2" ;;
  rm) rm -f "$d/device/$(basename "$4")" ;;
  dumpsys) if [ "$3" = window ]; then cat "$d/window"; else cat "$d/activities"; fi ;;
  am) ;;
  cmd)
    if [ "$3" = package ]; then
      for last; do :; done
      case "$*" in
      *category.HOME*) printf 'priority=0 preferredOrder=0 match=0x108000 specificIndex=-1 isDefault=true\ncom.google.android.apps.nexuslauncher/.NexusLauncherActivity\n' ;;
      *category.LAUNCHER*)
        if grep -qx "$last" "$d/launchable" 2>/dev/null; then printf 'priority=0 preferredOrder=0\n%s/.MainActivity\n' "$last"
        else echo "No activity found"; fi ;;
      esac
    else shift; PATH="$d/device-bin:$PATH" sh -c "$*"; fi ;;
  *) shift; PATH="$d/device-bin:$PATH" sh -c "$*" ;;
  esac ;;
pull)
  [ -f "$d/pull-time" ] && sleep "$(cat "$d/pull-time")"
  cp "$d/device/$(basename "$2")" "$3" ;;
install) echo "Success" ;;
forward)
  [ "$2" = --remove ] && exit 0
  [ -f "$d/forward-broken" ] && { echo "error: cannot bind listener" >&2; exit 1; }
  cat "$d/scrcpy-port" ;;
push) ;;
exec-out) cat "$d/screen.png" ;;
logcat) echo "logcat $*" ;;
esac
`

const (
	serial      = "emulator-5556"
	youtube     = "  topResumedActivity=ActivityRecord{6f3a1c2 u0 com.google.android.youtube/.HomeActivity t23}\n"
	launcher    = "  topResumedActivity=ActivityRecord{1b2c3d u0 com.google.android.apps.nexuslauncher/.NexusLauncherActivity t1}\n"
	openYouTube = "shell am start -a android.settings.APPLICATION_DETAILS_SETTINGS -d package:com.google.android.youtube"
)

var moment = time.Date(2026, 10, 11, 17, 0, 5, 0, time.Local)

// sdkWithADB makes a fake SDK whose platform-tools/adb is the fake adb; it returns the SDK and adb's folder.
func sdkWithADB(t *testing.T) (root, tools string) {
	t.Helper()
	root = fakeSDK(t, listingScript)
	tools = filepath.Join(root, "platform-tools")
	if err := os.MkdirAll(filepath.Join(tools, "device"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "adb"), []byte(fakeADB), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		recorders, _ := os.ReadFile(filepath.Join(tools, "recorders"))
		servers, _ := os.ReadFile(filepath.Join(tools, "servers"))
		for _, pid := range strings.Fields(string(recorders) + " " + string(servers)) {
			number, _ := strconv.Atoi(pid)
			syscall.Kill(number, syscall.SIGKILL)
		}
	})
	return root, tools
}

func atMoment(config *Config) { config.Now = func() time.Time { return moment } }

func adbCalls(tools string) []string {
	data, _ := os.ReadFile(filepath.Join(tools, "calls"))
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func control(t *testing.T, h *harness, path, body string) (int, string) {
	t.Helper()
	return send(t, h, http.MethodPost, path, "application/json", "", body)
}

func savedPath(t *testing.T, answer string) string {
	t.Helper()
	var saved struct{ Path string }
	if err := json.Unmarshal([]byte(answer), &saved); err != nil || saved.Path == "" {
		t.Fatalf("answer %q names no saved path", answer)
	}
	return saved.Path
}

func TestRotateTurnsTheDeviceAQuarter(t *testing.T) {
	h := startBridge(t)
	fake, _ := h.connected(t, testContext(t), "")

	for _, step := range []struct {
		from      []float32
		direction string
		want      []float32
	}{
		{[]float32{5, -3, 0}, "left", []float32{5, -3, 90}},
		{[]float32{5, -3, 90}, "right", []float32{5, -3, 0}},
		{[]float32{0, 0, 0}, "right", []float32{0, 0, -90}},
		{[]float32{0, 0, 180}, "left", []float32{0, 0, -90}},
		{[]float32{0, 0, 87}, "right", []float32{0, 0, 0}},
	} {
		fake.SetAngles(step.from...)
		if status, body := control(t, h, "/rotate", `{"direction":"`+step.direction+`"}`); status != http.StatusNoContent {
			t.Fatalf("rotate %s answered %d %s, want 204", step.direction, status, body)
		}
		if angles, _ := fake.Angles(); !reflect.DeepEqual(angles, step.want) {
			t.Errorf("rotate %s from %v: sensor %v, want %v", step.direction, step.from, angles, step.want)
		}
	}
}

func TestRotationsTogetherTurnTheDeviceTwice(t *testing.T) {
	h := startBridge(t)
	fake, _ := h.connected(t, testContext(t), "")
	fake.SetSensorReadTime(100 * time.Millisecond)

	var presses sync.WaitGroup
	for range 2 {
		presses.Go(func() {
			response, err := http.Post(h.server.URL+"/rotate", "application/json", strings.NewReader(`{"direction":"left"}`))
			if err != nil {
				t.Error(err)
				return
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Errorf("rotate answered %d, want 204", response.StatusCode)
			}
		})
	}
	presses.Wait()
	if angles, turns := fake.Angles(); angles[2] != -180 || turns != 2 {
		t.Errorf("two rotations left at once left the sensor at %v after %d sets, want z -180 after 2", angles, turns)
	}
}

func TestRotateRejectsAnUnknownDirection(t *testing.T) {
	h := startBridge(t)
	fake, _ := h.connected(t, testContext(t), "")

	for _, body := range []string{`{"direction":"up"}`, `{"direction":""}`, `{}`} {
		if status, _ := control(t, h, "/rotate", body); status != http.StatusBadRequest {
			t.Errorf("rotate %s answered %d, want 400", body, status)
		}
	}
	if _, turns := fake.Angles(); turns != 0 {
		t.Errorf("the sensor was set %d times by refused rotations", turns)
	}
}

func TestStreamFitsBothOrientations(t *testing.T) {
	h := startBridge(t)
	fake, _ := h.connected(t, testContext(t), "")

	asked := fake.Asked()
	if len(asked) == 0 {
		t.Fatal("no stream was opened")
	}
	side := uint32(streamWidth * deviceHeight / deviceWidth)
	if format := asked[0]; format.GetWidth() != side || format.GetHeight() != side {
		t.Errorf("stream asked for %d×%d, want the %d×%d square that holds the screen upright or on its side", format.GetWidth(), format.GetHeight(), side, side)
	}
}

func TestTouchFollowsTheRotationOfTheFrames(t *testing.T) {
	h := startBridge(t)
	fake, frames := h.connected(t, testContext(t), "")

	frames <- fakeemulator.Turned(fakeemulator.Frame(1, streamHeight, streamWidth, 0), pb.Rotation_LANDSCAPE)
	h.waitFor(t, func(state State) bool { return state.Rotation == device.Landscape })
	if status := postInput(t, h, "application/json", `{"type":"touch","phase":"down","x":0.25,"y":0.75}`); status != http.StatusNoContent {
		t.Fatalf("touch answered %d", status)
	}
	frames <- fakeemulator.Frame(2, streamWidth, streamHeight, 0)
	h.waitFor(t, func(state State) bool { return state.Rotation == device.Portrait })
	if status := postInput(t, h, "application/json", `{"type":"touch","phase":"down","x":0.25,"y":0.75}`); status != http.StatusNoContent {
		t.Fatalf("touch answered %d", status)
	}

	touches := fake.Touches()
	if len(touches) != 2 {
		t.Fatalf("emulator got %d touches, want 2", len(touches))
	}
	// Landscape (0.25, 0.75) is the panel's (0.25, 0.25): (2, 4) on 8×16; portrait passes through as (2, 11).
	for i, want := range [][2]int32{{2, 4}, {2, 11}} {
		if got := [2]int32{touches[i].GetX(), touches[i].GetY()}; got != want {
			t.Errorf("touch %d reached the panel at %v, want %v", i, got, want)
		}
	}
}

func TestScreenshotSavesThePNG(t *testing.T) {
	h := startBridge(t, atMoment)
	fake, _ := h.connected(t, testContext(t), "")
	fake.PNG = []byte("\x89PNG\r\n\x1a\nthe emulator's own bytes")

	status, answer := control(t, h, "/screenshot", `{}`)
	if status != http.StatusOK {
		t.Fatalf("screenshot answered %d %s, want 200", status, answer)
	}
	path := savedPath(t, answer)
	if want := filepath.Join(h.bridge.config.CapturesDir, "Screenshot_"+serial+"_20261011-170005.png"); path != want {
		t.Errorf("saved to %s, want %s", path, want)
	}
	if data, _ := os.ReadFile(path); string(data) != string(fake.PNG) {
		t.Errorf("saved %q, want the emulator's PNG byte for byte", data)
	}
	if state := h.status(t); state.Saved != path {
		t.Errorf("status says saved %q, want %q", state.Saved, path)
	}
}

func TestScreenshotsInOneSecondKeepBoth(t *testing.T) {
	h := startBridge(t, atMoment)
	fake, _ := h.connected(t, testContext(t), "")
	fake.PNG = []byte("first")

	_, first := control(t, h, "/screenshot", `{}`)
	fake.PNG = []byte("second")
	_, second := control(t, h, "/screenshot", `{}`)

	firstPath, secondPath := savedPath(t, first), savedPath(t, second)
	if want := strings.TrimSuffix(firstPath, ".png") + "_2.png"; secondPath != want {
		t.Errorf("second screenshot saved to %s, want %s", secondPath, want)
	}
	for path, want := range map[string]string{firstPath: "first", secondPath: "second"} {
		if data, _ := os.ReadFile(path); string(data) != want {
			t.Errorf("%s holds %q, want %q", path, data, want)
		}
	}
}

func TestRecordingStartsAndStops(t *testing.T) {
	root, tools := sdkWithADB(t)
	h := startBridge(t, withSDK(root), atMoment)
	h.connected(t, testContext(t), "")

	if status, body := control(t, h, "/recording/start", `{}`); status != http.StatusNoContent {
		t.Fatalf("start answered %d %s, want 204", status, body)
	}
	if state := h.status(t); !state.RecordingSince.Equal(moment) {
		t.Errorf("recording since %v, want %v", state.RecordingSince, moment)
	}
	eventually(t, func() bool { _, err := os.Stat(filepath.Join(tools, "recorders")); return err == nil }, "screenrecord running")
	if status, _ := control(t, h, "/recording/start", `{}`); status != http.StatusConflict {
		t.Errorf("a second start answered %d, want 409", status)
	}

	status, answer := control(t, h, "/recording/stop", `{}`)
	if status != http.StatusOK {
		t.Fatalf("stop answered %d %s, want 200", status, answer)
	}
	path := savedPath(t, answer)
	if want := filepath.Join(h.bridge.config.CapturesDir, "Screen_recording_"+serial+"_20261011-170005.mp4"); path != want {
		t.Errorf("saved to %s, want %s", path, want)
	}
	if data, _ := os.ReadFile(path); !strings.HasPrefix(string(data), "MP4 claude-recording-") {
		t.Errorf("saved %q, want the device's MP4", data)
	}
	if left, _ := os.ReadDir(filepath.Join(tools, "device")); len(left) != 0 {
		t.Errorf("the device still holds %v", left)
	}
	if state := h.status(t); !state.RecordingSince.IsZero() || state.Saved != path {
		t.Errorf("status after stop: recording since %v, saved %q; want no recording and %q", state.RecordingSince, state.Saved, path)
	}
	calls := adbCalls(tools)
	if !strings.HasPrefix(calls[0], "-s "+serial+" shell echo $$; exec screenrecord /sdcard/claude-recording-") {
		t.Errorf("first adb call %q, want screenrecord on %s", calls[0], serial)
	}
	recorder, _ := os.ReadFile(filepath.Join(tools, "recorders"))
	if !slices.ContainsFunc(calls, func(call string) bool {
		return strings.HasPrefix(call, "-s "+serial+" shell kill -INT "+strings.TrimSpace(string(recorder))+" ")
	}) {
		t.Errorf("adb calls %q interrupt no screenrecord by the pid it printed, %s", calls, recorder)
	}
}

func TestRecordingSurvivesAReconnect(t *testing.T) {
	root, tools := sdkWithADB(t)
	h := startBridge(t, withSDK(root), atMoment)
	ctx := testContext(t)
	first, _ := h.connected(t, ctx, "")
	if status, body := control(t, h, "/recording/start", `{}`); status != http.StatusNoContent {
		t.Fatalf("start answered %d %s, want 204", status, body)
	}

	first.Stop()
	os.Remove(filepath.Join(h.dir, "pid_1.ini"))
	if state := h.waitFor(t, func(state State) bool { return !state.Connected }); !state.RecordingSince.Equal(moment) {
		t.Errorf("while the emulator is away, recording since %v, want %v", state.RecordingSince, moment)
	}
	second := fakeemulator.Start(t, deviceWidth, deviceHeight, "")
	second.WriteINI(t, h.dir, 1)
	second.NextStream(t, ctx)
	if state := h.waitFor(t, func(state State) bool { return state.Connected }); !state.RecordingSince.Equal(moment) {
		t.Errorf("after the reconnect, recording since %v, want %v", state.RecordingSince, moment)
	}

	status, answer := control(t, h, "/recording/stop", `{}`)
	if status != http.StatusOK {
		t.Fatalf("stop after the reconnect answered %d %s, want 200", status, answer)
	}
	if data, _ := os.ReadFile(savedPath(t, answer)); !strings.HasPrefix(string(data), "MP4 claude-recording-") {
		t.Errorf("saved %q, want the device's MP4", data)
	}
	if left, _ := os.ReadDir(filepath.Join(tools, "device")); len(left) != 0 {
		t.Errorf("the device still holds %v", left)
	}
}

func TestStopAsTheRecordingEndsByItself(t *testing.T) {
	root, tools := sdkWithADB(t)
	writeFile(t, filepath.Join(tools, "limit"), "0.1")
	writeFile(t, filepath.Join(tools, "pull-time"), "1")
	h := startBridge(t, withSDK(root), atMoment)
	h.connected(t, testContext(t), "")

	if status, body := control(t, h, "/recording/start", `{}`); status != http.StatusNoContent {
		t.Fatalf("start answered %d %s, want 204", status, body)
	}
	eventually(t, func() bool { left, _ := os.ReadDir(filepath.Join(tools, "device")); return len(left) > 0 }, "the recording ending by itself")
	status, answer := control(t, h, "/recording/stop", `{}`)
	if status != http.StatusOK {
		t.Fatalf("stop while the ended recording is saved answered %d %s, want 200", status, answer)
	}
	if data, _ := os.ReadFile(savedPath(t, answer)); !strings.HasPrefix(string(data), "MP4 claude-recording-") {
		t.Errorf("saved %q, want the device's MP4", data)
	}
}

func TestRecordingThatFailsSaysWhy(t *testing.T) {
	root, tools := sdkWithADB(t)
	writeFile(t, filepath.Join(tools, "broken"), "ERROR: unable to create video encoder")
	h := startBridge(t, withSDK(root), atMoment)
	h.connected(t, testContext(t), "")

	if status, body := control(t, h, "/recording/start", `{}`); status != http.StatusNoContent {
		t.Fatalf("start answered %d %s, want 204", status, body)
	}
	state := h.waitFor(t, func(state State) bool { return state.RecordingSince.IsZero() })
	if !strings.Contains(state.RecordingError, "unable to create video encoder") || state.Saved != "" {
		t.Errorf("status after a failed recording: error %q, saved %q; want screenrecord's error and nothing saved", state.RecordingError, state.Saved)
	}
	if saved, _ := os.ReadDir(h.bridge.config.CapturesDir); len(saved) != 0 {
		t.Errorf("a failed recording left %v", saved)
	}
}

func TestRecordingOnAnOfflineDevice(t *testing.T) {
	root, tools := sdkWithADB(t)
	writeFile(t, filepath.Join(tools, "offline"), "")
	h := startBridge(t, withSDK(root), atMoment)
	h.connected(t, testContext(t), "")

	if status, body := control(t, h, "/recording/start", `{}`); status != http.StatusBadGateway || !strings.Contains(body, "device offline") {
		t.Errorf("start on an offline device answered %d %q, want 502 naming adb's error", status, body)
	}
	if state := h.status(t); !state.RecordingSince.IsZero() || !strings.Contains(state.RecordingError, "device offline") {
		t.Errorf("status after the failed start: recording since %v, error %q", state.RecordingSince, state.RecordingError)
	}
	if status, _ := control(t, h, "/recording/stop", `{}`); status != http.StatusConflict {
		t.Errorf("stop after the failed start answered %d, want 409", status)
	}
}

func TestRecordingThatEndsByItselfIsSaved(t *testing.T) {
	root, tools := sdkWithADB(t)
	writeFile(t, filepath.Join(tools, "limit"), "0.3")
	h := startBridge(t, withSDK(root), atMoment)
	h.connected(t, testContext(t), "")

	if status, body := control(t, h, "/recording/start", `{}`); status != http.StatusNoContent {
		t.Fatalf("start answered %d %s, want 204", status, body)
	}
	state := h.waitFor(t, func(state State) bool { return state.RecordingSince.IsZero() && state.Saved != "" })
	if data, _ := os.ReadFile(state.Saved); !strings.HasPrefix(string(data), "MP4 claude-recording-") {
		t.Errorf("saved %q to %s, want the device's MP4", data, state.Saved)
	}
	if status, _ := control(t, h, "/recording/stop", `{}`); status != http.StatusConflict {
		t.Errorf("stop after the recording ended answered %d, want 409", status)
	}
}

func TestRecordingWithoutADB(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	h := startBridge(t)
	h.connected(t, testContext(t), "")

	if status, body := control(t, h, "/recording/start", `{}`); status != http.StatusServiceUnavailable || !strings.Contains(body, "adb") {
		t.Errorf("start without adb answered %d %q, want 503 naming adb", status, body)
	}
}

func TestAppSettingsOpensTheAppInFront(t *testing.T) {
	root, tools := sdkWithADB(t)
	writeFile(t, filepath.Join(tools, "activities"), youtube)
	h := startBridge(t, withSDK(root))
	h.connected(t, testContext(t), "")

	if status, body := control(t, h, "/app-settings", `{}`); status != http.StatusNoContent {
		t.Fatalf("app settings answered %d %s, want 204", status, body)
	}
	if calls := adbCalls(tools); calls[len(calls)-1] != "-s "+serial+" "+openYouTube {
		t.Errorf("last adb call %q, want App info of YouTube", calls[len(calls)-1])
	}
}

func TestAppSettingsWithTheHomeScreenInFront(t *testing.T) {
	root, tools := sdkWithADB(t)
	writeFile(t, filepath.Join(tools, "activities"), launcher)
	h := startBridge(t, withSDK(root))
	h.connected(t, testContext(t), "")

	if status, _ := control(t, h, "/app-settings", `{}`); status != http.StatusConflict {
		t.Errorf("app settings with the home screen in front answered %d, want 409", status)
	}
	for _, call := range adbCalls(tools) {
		if strings.Contains(call, " am start ") {
			t.Errorf("adb started %q with no app in front", call)
		}
	}
}

func TestControlRoutesRejectForeignAndNonJSONRequests(t *testing.T) {
	root, tools := sdkWithADB(t)
	writeFile(t, filepath.Join(tools, "activities"), youtube)
	h := startBridge(t, withSDK(root))
	fake, _ := h.connected(t, testContext(t), "")
	fake.PNG = []byte("png")

	for _, route := range []struct{ path, body string }{
		{"/rotate", `{"direction":"left"}`},
		{"/screenshot", `{}`},
		{"/recording/start", `{}`},
		{"/recording/stop", `{}`},
		{"/app-settings", `{}`},
	} {
		if status, _ := send(t, h, http.MethodPost, route.path, "text/plain", "", route.body); status != http.StatusUnsupportedMediaType {
			t.Errorf("%s as text/plain answered %d, want 415", route.path, status)
		}
		if status, _ := send(t, h, http.MethodPost, route.path, "application/json", "http://evil.example", route.body); status != http.StatusForbidden {
			t.Errorf("%s from a foreign Origin answered %d, want 403", route.path, status)
		}
	}
	if _, turns := fake.Angles(); turns != 0 {
		t.Errorf("refused requests turned the device %d times", turns)
	}
	if saved, _ := os.ReadDir(h.bridge.config.CapturesDir); len(saved) != 0 {
		t.Errorf("refused requests saved %v", saved)
	}
	if _, err := os.Stat(filepath.Join(tools, "calls")); err == nil {
		t.Errorf("refused requests ran adb: %v", adbCalls(tools))
	}
}
