package live

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/adb"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/emulator"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/sdk"
)

const (
	// Claude reads screenshots no larger than this on their long side, and taps in their pixels.
	screenLongSide = 1280
	screenQuality  = 85
	// A swipe moves the finger at about the display's frame rate.
	swipeStep          = 16 * time.Millisecond
	defaultTapTime     = 50 * time.Millisecond
	defaultSwipeTime   = 300 * time.Millisecond
	maxGestureTime     = 10 * time.Second
	defaultLogLines    = 200
	maxLogLines        = 2000
	statusWaitTimeout  = 25 * time.Second
	launchAppTimeout   = 2 * time.Minute
	gestureRouteMargin = 5 * time.Second
)

var (
	// ErrNotRunning is returned for the logs of an app that has no process.
	ErrNotRunning = errors.New("the app is not running")
	// ErrNoLauncherActivity is returned for an app with nothing to start from the launcher.
	ErrNoLauncherActivity = errors.New("the app has no launcher activity")
)

// Screen is a screenshot for Claude: a base64 JPEG small enough to read whole, in whose pixels taps and swipes are given.
type Screen struct {
	JPEG     string            `json:"jpeg"`
	Width    int               `json:"width"`
	Height   int               `json:"height"`
	Rotation emulator.Rotation `json:"rotation"`
}

// Gesture is a tap, a long press or a swipe in a Screen's pixels; a tap has no end.
type Gesture struct {
	X  *float64 `json:"x"`
	Y  *float64 `json:"y"`
	X2 *float64 `json:"x2"`
	Y2 *float64 `json:"y2"`
	// Duration is how long the finger stays down, in seconds.
	Duration float64 `json:"duration"`
}

// LaunchRequest names the app to start, the APK to install first, or both.
type LaunchRequest struct {
	Package string `json:"package"`
	APK     string `json:"apk"`
}

// screenSize is the size Screen draws the upright screen at, never larger than the device.
func screenSize(upright emulator.Size) emulator.Size {
	scale := min(1, float64(screenLongSide)/float64(max(upright.Width, upright.Height)))
	return emulator.Size{
		Width:  int(math.Round(float64(upright.Width) * scale)),
		Height: int(math.Round(float64(upright.Height) * scale)),
	}
}

// Screen takes a screenshot scaled for Claude.
func (b *Bridge) Screen(ctx context.Context) (Screen, error) {
	conn := b.connection()
	if conn == nil || !b.State().Booted {
		return Screen{}, ErrNoEmulator
	}
	frame, err := conn.ScreenshotAt(ctx, screenSize(conn.Upright()))
	if err != nil {
		return Screen{}, err
	}
	pixels := image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height))
	for from, to := 0, 0; from < len(frame.RGB); from, to = from+3, to+4 {
		copy(pixels.Pix[to:to+3], frame.RGB[from:from+3])
		pixels.Pix[to+3] = 0xff
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, pixels, &jpeg.Options{Quality: screenQuality}); err != nil {
		return Screen{}, err
	}

	return Screen{JPEG: base64.StdEncoding.EncodeToString(encoded.Bytes()), Width: frame.Width, Height: frame.Height, Rotation: frame.Rotation}, nil
}

// Gesture presses where the gesture starts, moves to its end if it has one, and lifts after its duration.
func (b *Bridge) Gesture(ctx context.Context, gesture Gesture) error {
	conn := b.connection()
	if conn == nil {
		return ErrNoEmulator
	}
	size := screenSize(conn.Upright())
	isSwipe := gesture.X2 != nil || gesture.Y2 != nil
	if gesture.X == nil || gesture.Y == nil || isSwipe && (gesture.X2 == nil || gesture.Y2 == nil) {
		return fmt.Errorf("%w: a gesture needs x and y, and a swipe x2 and y2 too", ErrBadInput)
	}
	startX, startY := *gesture.X, *gesture.Y
	points := [][2]float64{{startX, startY}}
	if isSwipe {
		points = append(points, [2]float64{*gesture.X2, *gesture.Y2})
	}
	for _, point := range points {
		if !(point[0] >= 0 && point[0] < float64(size.Width) && point[1] >= 0 && point[1] < float64(size.Height)) {
			return fmt.Errorf("%w: (%v, %v) is outside the %d×%d screen", ErrBadInput, point[0], point[1], size.Width, size.Height)
		}
	}
	held := time.Duration(gesture.Duration * float64(time.Second))
	if held == 0 {
		held = defaultTapTime
		if isSwipe {
			held = defaultSwipeTime
		}
	}
	if !(held >= 0 && held <= maxGestureTime) {
		return fmt.Errorf("%w: duration %vs is outside 0–%v", ErrBadInput, gesture.Duration, maxGestureTime)
	}
	at := func(ctx context.Context, x, y float64, isDown bool) error {
		return conn.Touch(ctx, x/float64(size.Width), y/float64(size.Height), isDown)
	}
	endX, endY := startX, startY
	if isSwipe {
		endX, endY = *gesture.X2, *gesture.Y2
	}
	// The finger lifts even when ctx ends mid-gesture, so the device is not left touched.
	lift := func() error {
		liftCtx, cancel := context.WithTimeout(context.Background(), inputTimeout)
		defer cancel()
		return at(liftCtx, endX, endY, false)
	}
	if err := at(ctx, startX, startY, true); err != nil {
		return err
	}
	steps := max(1, int(held/swipeStep))
	for step := 1; step <= steps; step++ {
		if err := sleepCtx(ctx, held/time.Duration(steps)); err != nil {
			lift()
			return err
		}
		if isSwipe {
			share := float64(step) / float64(steps)
			if err := at(ctx, startX+(endX-startX)*share, startY+(endY-startY)*share, true); err != nil {
				lift()
				return err
			}
		}
	}

	return lift()
}

// OpenURL opens url on the device as tapping a link does: in the app that handles it.
func (b *Bridge) OpenURL(ctx context.Context, link string) error {
	parsed, err := url.Parse(link)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf("%w: %q is not a URL with a scheme", ErrBadInput, link)
	}
	device, err := b.adbDevice()
	if err != nil {
		return err
	}
	_, err = device.Run(ctx, "shell", "am start -W -a android.intent.action.VIEW -d "+adb.Quote(link))

	return err
}

// LaunchApp installs the APK when one is given, then starts the app's launcher activity; it answers the package.
func (b *Bridge) LaunchApp(ctx context.Context, request LaunchRequest) (string, error) {
	if request.Package != "" && !adb.IsPackageName(request.Package) {
		return "", fmt.Errorf("%w: %q is not an application id", ErrBadInput, request.Package)
	}
	if request.Package == "" && request.APK == "" {
		return "", fmt.Errorf("%w: name a package, an apk, or both", ErrBadInput)
	}
	device, err := b.adbDevice()
	if err != nil {
		return "", err
	}
	name := request.Package
	if request.APK != "" {
		if !filepath.IsAbs(request.APK) || !strings.EqualFold(filepath.Ext(request.APK), ".apk") {
			return "", fmt.Errorf("%w: apk must be the absolute path of an .apk file, not %q", ErrBadInput, request.APK)
		}
		if info, err := os.Stat(request.APK); err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("%w: no APK at %s", ErrBadInput, request.APK)
		}
		if name == "" {
			if name, err = b.apkPackage(ctx, request.APK); err != nil {
				return "", err
			}
		}
		if _, err := device.Run(ctx, "install", "-r", "-t", request.APK); err != nil {
			return "", err
		}
	}
	resolved, err := device.Run(ctx, "shell", "cmd", "package", "resolve-activity", "--brief",
		"-a", "android.intent.action.MAIN", "-c", "android.intent.category.LAUNCHER", name)
	if err != nil {
		return "", err
	}
	component := adb.ResolvedComponent(resolved)
	if component == "" {
		return "", fmt.Errorf("%w: %s", ErrNoLauncherActivity, name)
	}
	if _, err := device.Run(ctx, "shell", "am start -W -n "+adb.Quote(component)); err != nil {
		return "", err
	}

	return name, nil
}

// apkPackage reads the APK's application id with aapt2 from the SDK's newest build-tools.
func (b *Bridge) apkPackage(ctx context.Context, apk string) (string, error) {
	found, err := sdk.Find(b.config.SDKCandidates, runtime.GOOS)
	if err != nil {
		return "", err
	}
	tools, _ := filepath.Glob(filepath.Join(found.Root, "build-tools", "*", executable("aapt2")))
	slices.SortFunc(tools, func(a, b string) int {
		return compareVersions(filepath.Base(filepath.Dir(a)), filepath.Base(filepath.Dir(b)))
	})
	if len(tools) == 0 {
		return "", fmt.Errorf("%w: the SDK has no build-tools to read the APK's application id; name the package too", ErrBadInput)
	}
	output, err := exec.CommandContext(ctx, tools[len(tools)-1], "dump", "packagename", apk).Output()
	name := strings.TrimSpace(string(output))
	if err != nil || !adb.IsPackageName(name) {
		return "", fmt.Errorf("%w: cannot read the application id of %s; name the package too", ErrBadInput, apk)
	}

	return name, nil
}

// Logs answers the device log's newest lines, only the app's when a package is named.
func (b *Bridge) Logs(ctx context.Context, name string, lines int) (string, error) {
	if lines == 0 {
		lines = defaultLogLines
	}
	if lines < 1 || lines > maxLogLines {
		return "", fmt.Errorf("%w: lines %d is outside 1–%d", ErrBadInput, lines, maxLogLines)
	}
	if name != "" && !adb.IsPackageName(name) {
		return "", fmt.Errorf("%w: %q is not an application id", ErrBadInput, name)
	}
	device, err := b.adbDevice()
	if err != nil {
		return "", err
	}
	args := []string{"logcat", "-d", "-t", strconv.Itoa(lines)}
	if name != "" {
		pid, _ := device.Run(ctx, "shell", "pidof -s "+name+"; true")
		if _, err := strconv.Atoi(strings.TrimSpace(pid)); err != nil {
			return "", fmt.Errorf("%w: %s", ErrNotRunning, name)
		}
		args = append(args, "--pid="+strings.TrimSpace(pid))
	}

	return device.Run(ctx, args...)
}

// WaitBooted answers the state once the emulator has booted, its launch failed, nothing is starting, or ctx ends.
func (b *Bridge) WaitBooted(ctx context.Context) State {
	changes, stop := b.watch()
	defer stop()
	for {
		state := b.State()
		isStarting := state.Connected || state.Launch.Phase == launchStarting
		if state.Booted || !isStarting {
			return state
		}
		select {
		case <-changes:
		case <-ctx.Done():
			return state
		}
	}
}

func sleepCtx(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func executable(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// compareVersions orders dotted version numbers by their numbers, 36.1.0 after 9.0.0.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x - y
		}
	}
	return 0
}
