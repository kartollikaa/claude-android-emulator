//go:build unix

package live

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/emulator"
	pb "github.com/kartollikaa/claude-android-emulator/bridge/internal/emulatorpb"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/fakeemulator"
)

// The fake device's am, pidof and logcat answers; am writes each argument it got on a line of am-args.
var agentDeviceCommands = map[string]string{
	"am": `#!/bin/sh
printf '%s\n' "$@" > "$(dirname "$0")/../am-args"
`,
	"pidof": `#!/bin/sh
for last; do :; done
grep -qx "$last" "$(dirname "$0")/../running" 2>/dev/null && echo 4242
`,
}

func agentDevice(t *testing.T) (*harness, *fakeemulator.Fake, chan<- *pb.Image, string) {
	t.Helper()
	root, tools := sdkWithADB(t)
	if err := os.MkdirAll(filepath.Join(tools, "device-bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, script := range agentDeviceCommands {
		if err := os.WriteFile(filepath.Join(tools, "device-bin", name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h := startBridge(t, withSDK(root))
	fake, frames := h.connected(t, testContext(t), "")
	h.waitFor(t, func(state State) bool { return state.Booted })

	return h, fake, frames, tools
}

func touchesAt(touches []*pb.Touch) [][3]int32 {
	var points [][3]int32
	for _, touch := range touches {
		points = append(points, [3]int32{touch.GetX(), touch.GetY(), touch.GetPressure()})
	}
	return points
}

func TestScreenIsAJPEGOfTheUprightScreen(t *testing.T) {
	h, fake, _, _ := agentDevice(t)

	status, answer := send(t, h, http.MethodGet, "/screen", "", "", "")
	var screen Screen
	if err := json.Unmarshal([]byte(answer), &screen); status != http.StatusOK || err != nil {
		t.Fatalf("answered %d %s", status, answer)
	}
	data, _ := base64.StdEncoding.DecodeString(screen.JPEG)
	picture, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the screen is not a JPEG: %v", err)
	}
	if bounds := picture.Bounds(); bounds.Dx() != deviceWidth || bounds.Dy() != deviceHeight || screen.Width != deviceWidth || screen.Height != deviceHeight {
		t.Errorf("screen %d×%d, picture %v, want %d×%d", screen.Width, screen.Height, bounds, deviceWidth, deviceHeight)
	}
	asked := fake.Screenshots()
	if len(asked) != 1 || asked[0].GetFormat() != pb.ImageFormat_RGB888 || asked[0].GetWidth() != deviceWidth || asked[0].GetHeight() != deviceHeight {
		t.Errorf("asked the emulator for %v, want one RGB888 screenshot at the screen's size", asked)
	}
}

func TestScreenWithoutAnEmulator(t *testing.T) {
	h := startBridge(t)

	if status, _ := send(t, h, http.MethodGet, "/screen", "", "", ""); status != http.StatusServiceUnavailable {
		t.Errorf("a screen with no emulator answered %d, want 503", status)
	}
}

func TestScreenWhileTheEmulatorBoots(t *testing.T) {
	h := startBridge(t)
	fake := fakeemulator.Start(t, deviceWidth, deviceHeight, "")
	fake.SetBooted(false)
	fake.WriteINI(t, h.dir, 1)
	h.waitFor(t, func(state State) bool { return state.Connected })

	if status, _ := send(t, h, http.MethodGet, "/screen", "", "", ""); status != http.StatusServiceUnavailable {
		t.Errorf("a screen of a booting emulator answered %d, want 503", status)
	}
}

func TestTapPressesAndLifts(t *testing.T) {
	h, fake, _, _ := agentDevice(t)

	if status, answer := control(t, h, "/gesture", `{"x": 2, "y": 4}`); status != http.StatusNoContent {
		t.Fatalf("tap answered %d %s", status, answer)
	}
	// (2, 4) of the 8×16 screen is (0.25, 0.25), the panel's pixel (2, 4).
	if got, want := touchesAt(fake.Touches()), [][3]int32{{2, 4, 1}, {2, 4, 0}}; !reflect.DeepEqual(got, want) {
		t.Errorf("touches %v, want %v", got, want)
	}
}

func TestLongPressHoldsForItsDuration(t *testing.T) {
	h, fake, _, _ := agentDevice(t)

	started := time.Now()
	if status, answer := control(t, h, "/gesture", `{"x": 2, "y": 4, "duration": 0.3}`); status != http.StatusNoContent {
		t.Fatalf("long press answered %d %s", status, answer)
	}
	if held := time.Since(started); held < 300*time.Millisecond {
		t.Errorf("the long press lifted after %v", held)
	}
	if got := touchesAt(fake.Touches()); len(got) != 2 || got[0][2] != 1 || got[1][2] != 0 {
		t.Errorf("touches %v, want one press and one lift", got)
	}
}

func TestSwipeMovesBetweenItsEnds(t *testing.T) {
	h, fake, _, _ := agentDevice(t)

	if status, answer := control(t, h, "/gesture", `{"x": 0, "y": 0, "x2": 7, "y2": 15, "duration": 0.1}`); status != http.StatusNoContent {
		t.Fatalf("swipe answered %d %s", status, answer)
	}
	got := touchesAt(fake.Touches())
	if len(got) < 4 || got[0] != [3]int32{0, 0, 1} || got[len(got)-1] != [3]int32{6, 14, 0} {
		t.Fatalf("touches %v, want a press at the start, moves, and a lift at the end", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i][0] < got[i-1][0] || got[i][1] < got[i-1][1] {
			t.Errorf("touch %d %v moves back from %v", i, got[i], got[i-1])
		}
	}
}

func TestGestureFollowsTheRotation(t *testing.T) {
	h, fake, frames, _ := agentDevice(t)
	frames <- fakeemulator.Turned(fakeemulator.Frame(1, streamHeight, streamWidth, 0), pb.Rotation_LANDSCAPE)
	h.waitFor(t, func(state State) bool { return state.Rotation == emulator.Landscape })

	if status, answer := control(t, h, "/gesture", `{"x": 4, "y": 2}`); status != http.StatusNoContent {
		t.Fatalf("tap answered %d %s", status, answer)
	}
	// (4, 2) of the 16×8 landscape screen is (0.25, 0.25), the panel's (0.75, 0.25): pixel (5, 4).
	if got := touchesAt(fake.Touches()); len(got) != 2 || got[0] != [3]int32{5, 4, 1} {
		t.Errorf("touches %v, want a press at the panel's (5, 4)", got)
	}
}

func TestGestureRejectsBadPoints(t *testing.T) {
	h, fake, _, _ := agentDevice(t)

	for _, body := range []string{
		`{"x": 8, "y": 4}`, `{"x": -1, "y": 4}`, `{"x": 2}`, `{"x": 2, "y": 4, "x2": 3}`,
		`{"x": 2, "y": 4, "x2": 3, "y2": 16}`, `{"x": 2, "y": 4, "duration": 11}`, `{"x": 2, "y": 4, "duration": -1}`,
	} {
		if status, answer := control(t, h, "/gesture", body); status != http.StatusBadRequest {
			t.Errorf("%s answered %d %s, want 400", body, status, answer)
		}
	}
	if touches := fake.Touches(); len(touches) != 0 {
		t.Errorf("refused gestures touched the screen %v", touchesAt(touches))
	}
}

func TestOpenURLPassesTheLinkWhole(t *testing.T) {
	h, _, _, tools := agentDevice(t)
	link := `https://example.com/a?b=1&c=$HOME;d='e'`

	body, _ := json.Marshal(map[string]string{"url": link})
	if status, answer := control(t, h, "/open-url", string(body)); status != http.StatusNoContent {
		t.Fatalf("open-url answered %d %s", status, answer)
	}
	args, _ := os.ReadFile(filepath.Join(tools, "am-args"))
	if want := "start\n-W\n-a\nandroid.intent.action.VIEW\n-d\n" + link + "\n"; string(args) != want {
		t.Errorf("am got\n%s\nwant\n%s", args, want)
	}
}

func TestOpenURLRejectsWhatIsNotAURL(t *testing.T) {
	h, _, _, tools := agentDevice(t)

	for _, link := range []string{"", "no scheme", "https://x\nreboot"} {
		body, _ := json.Marshal(map[string]string{"url": link})
		if status, answer := control(t, h, "/open-url", string(body)); status != http.StatusBadRequest {
			t.Errorf("url %q answered %d %s, want 400", link, status, answer)
		}
	}
	if calls := adbCalls(tools); len(calls) != 1 || calls[0] != "" {
		t.Errorf("refused links ran adb: %q", calls)
	}
}

func TestLaunchStartsTheLauncherActivity(t *testing.T) {
	h, _, _, tools := agentDevice(t)
	writeFile(t, filepath.Join(tools, "launchable"), "com.example.app\n")

	status, answer := control(t, h, "/launch", `{"package": "com.example.app"}`)
	if status != http.StatusOK || !strings.Contains(answer, `"package":"com.example.app"`) {
		t.Fatalf("launch answered %d %s", status, answer)
	}
	if args, _ := os.ReadFile(filepath.Join(tools, "am-args")); string(args) != "start\n-W\n-n\ncom.example.app/.MainActivity\n" {
		t.Errorf("am got %q, want the app's launcher activity", args)
	}
}

func TestLaunchInstallsTheAPKFirst(t *testing.T) {
	h, _, _, tools := agentDevice(t)
	writeFile(t, filepath.Join(tools, "launchable"), "com.example.app\n")
	root := filepath.Dir(tools)
	for version, name := range map[string]string{"9.0.0": "com.example.old", "36.1.0": "com.example.app"} {
		dir := filepath.Join(root, "build-tools", version)
		os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(filepath.Join(dir, "aapt2"), []byte("#!/bin/sh\necho "+name+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	apk := filepath.Join(t.TempDir(), "app-debug.apk")
	writeFile(t, apk, "apk")

	body, _ := json.Marshal(map[string]string{"apk": apk})
	status, answer := control(t, h, "/launch", string(body))
	if status != http.StatusOK || !strings.Contains(answer, `"package":"com.example.app"`) {
		t.Fatalf("launch answered %d %s, want the newest build-tools' reading of the APK", status, answer)
	}
	calls := adbCalls(tools)
	if !reflect.DeepEqual(calls[0], "-s "+serial+" install -r -t "+apk) {
		t.Errorf("first adb call %q, want the install", calls[0])
	}

	writeFile(t, filepath.Join(root, "build-tools", "36.1.0", "aapt2"), "#!/bin/sh\necho 'app;reboot'\n")
	if status, answer := control(t, h, "/launch", string(body)); status != http.StatusBadRequest {
		t.Errorf("an APK whose id aapt2 misreads answered %d %s, want 400", status, answer)
	}
	if len(adbCalls(tools)) != len(calls) {
		t.Errorf("a misread APK ran adb: %q", adbCalls(tools)[len(calls):])
	}
}

func TestLaunchRejectsBadRequests(t *testing.T) {
	h, _, _, tools := agentDevice(t)

	for _, body := range []string{`{}`, `{"package": "app;reboot"}`, `{"apk": "relative.apk"}`, `{"apk": "/no/such/app.apk"}`, `{"apk": "/etc/hosts"}`} {
		if status, answer := control(t, h, "/launch", body); status != http.StatusBadRequest {
			t.Errorf("%s answered %d %s, want 400", body, status, answer)
		}
	}
	cwd, _ := os.Getwd()
	apk := filepath.Join(t.TempDir(), "app.apk")
	writeFile(t, apk, "apk")
	relative, err := filepath.Rel(cwd, apk)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"apk": relative, "package": "com.example.app"})
	if status, answer := control(t, h, "/launch", string(body)); status != http.StatusBadRequest {
		t.Errorf("an APK named by a relative path answered %d %s, want 400", status, answer)
	}
	if calls := adbCalls(tools); len(calls) != 1 || calls[0] != "" {
		t.Errorf("refused launches ran adb: %q", calls)
	}
	if status, answer := control(t, h, "/launch", `{"package": "com.example.none"}`); status != http.StatusConflict {
		t.Errorf("an app without a launcher activity answered %d %s, want 409", status, answer)
	}
}

func TestLogsOfARunningApp(t *testing.T) {
	h, _, _, tools := agentDevice(t)
	writeFile(t, filepath.Join(tools, "running"), "com.example.app\n")

	status, answer := control(t, h, "/logs", `{"package": "com.example.app", "lines": 20}`)
	if status != http.StatusOK || !strings.Contains(answer, "logcat -d -t 20 --pid=4242") {
		t.Errorf("logs answered %d %s, want the app's 20 newest lines", status, answer)
	}
	if status, answer := control(t, h, "/logs", `{}`); status != http.StatusOK || !strings.Contains(answer, "logcat -d -t 200\\n") {
		t.Errorf("all logs answered %d %s, want the device's 200 newest lines", status, answer)
	}
	if status, _ := control(t, h, "/logs", `{"package": "com.example.idle"}`); status != http.StatusConflict {
		t.Errorf("the logs of an app that is not running answered %d, want 409", status)
	}
	for _, body := range []string{`{"lines": 5000}`, `{"lines": -1}`, `{"package": "x;reboot"}`} {
		if status, _ := control(t, h, "/logs", body); status != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400", body, status)
		}
	}
}

func TestStatusWaitsForTheBoot(t *testing.T) {
	h := startBridge(t)
	asked := time.Now()
	if status, answer := send(t, h, http.MethodGet, "/status?wait=booted", "", "", ""); status != http.StatusOK || !strings.Contains(answer, `"connected":false`) || time.Since(asked) > statusWaitTimeout/2 {
		t.Fatalf("with nothing starting the wait answered %d %s after %v, want the state at once", status, answer, time.Since(asked))
	}
	fake := fakeemulator.Start(t, deviceWidth, deviceHeight, "")
	fake.SetBooted(false)
	fake.WriteINI(t, h.dir, 1)
	h.waitFor(t, func(state State) bool { return state.Connected })

	time.AfterFunc(200*time.Millisecond, func() { fake.SetBooted(true) })
	started := time.Now()
	status, answer := send(t, h, http.MethodGet, "/status?wait=booted", "", "", "")
	if status != http.StatusOK || !strings.Contains(answer, `"booted":true`) || time.Since(started) < 150*time.Millisecond {
		t.Errorf("the wait answered %d %s after %v, want the booted state once it booted", status, answer, time.Since(started))
	}
}

func TestToolRoutesRefuseForeignAndNonJSONRequests(t *testing.T) {
	h, fake, _, tools := agentDevice(t)

	for _, route := range []struct{ path, body string }{
		{"/gesture", `{"x": 2, "y": 4}`},
		{"/open-url", `{"url": "https://example.com"}`},
		{"/launch", `{"package": "com.example.app"}`},
		{"/logs", `{}`},
	} {
		if status, _ := send(t, h, http.MethodPost, route.path, "text/plain", "", route.body); status != http.StatusUnsupportedMediaType {
			t.Errorf("%s as text/plain answered %d, want 415", route.path, status)
		}
		if status, _ := send(t, h, http.MethodPost, route.path, "application/json", "http://evil.example", route.body); status != http.StatusForbidden {
			t.Errorf("%s from a foreign Origin answered %d, want 403", route.path, status)
		}
	}
	if status, _ := send(t, h, http.MethodGet, "/screen", "", "http://evil.example", ""); status != http.StatusForbidden {
		t.Errorf("GET /screen from a foreign Origin answered %d, want 403", status)
	}
	if touches := fake.Touches(); len(touches) != 0 {
		t.Errorf("refused requests touched the screen")
	}
	if calls := adbCalls(tools); len(calls) != 1 || calls[0] != "" {
		t.Errorf("refused requests ran adb: %q", calls)
	}
}

func TestFirstStatusSeesTheRunningEmulator(t *testing.T) {
	dir := t.TempDir()
	fake := fakeemulator.Start(t, deviceWidth, deviceHeight, "")
	fake.WriteINI(t, dir, 1)
	h := startBridge(t, func(config *Config) { config.DiscoveryDir = dir })

	if state := h.status(t); !state.Connected {
		t.Errorf("the first status of a fresh bridge %+v, want the running emulator connected", state)
	}
}
