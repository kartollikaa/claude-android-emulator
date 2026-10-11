package live

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/adb"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/device"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/sdk"
)

const (
	// The device caps a recording's length, so pulling one is bounded too.
	recordingSaveTimeout = 30 * time.Second
	// Stopping waits for screenrecord to finish the MP4 and then for the save.
	recordingStopTimeout = sdkTimeout + recordingSaveTimeout
)

var (
	// ErrRecording is returned for a start while a screen recording runs, ErrNotRecording for a stop without one.
	ErrRecording    = errors.New("a screen recording is already running")
	ErrNotRecording = errors.New("no screen recording is running")
	// ErrHomeInFront is returned for App info while no app is in front.
	ErrHomeInFront = errors.New("no app is in front")
	// ErrNoADB is returned when neither the SDK nor PATH has adb.
	ErrNoADB = errors.New("adb was not found in the Android SDK's platform-tools or on PATH")
)

type recording struct {
	device     adb.Device
	devicePath string
	// pid is screenrecord's on the device, known once started closes; empty if it never ran.
	pid     string
	started chan struct{}
	done    chan struct{}
	saved   string
	err     error
}

func defaultCapturesDir(home string) string {
	desktop := filepath.Join(home, "Desktop")
	if info, err := os.Stat(desktop); err == nil && info.IsDir() {
		return desktop
	}

	return home
}

// Rotate turns the emulator a quarter to the left (counter-clockwise) or to the right.
func (b *Bridge) Rotate(ctx context.Context, direction string) error {
	quarters := map[string]int{"left": 1, "right": -1}[direction]
	if quarters == 0 {
		return fmt.Errorf("%w: rotate left or right, not %q", ErrBadInput, direction)
	}
	conn := b.connection()
	if conn == nil {
		return ErrNoEmulator
	}

	return conn.Rotate(ctx, quarters)
}

// Screenshot saves the emulator's screen as the PNG it encodes into the captures directory and returns its path.
func (b *Bridge) Screenshot(ctx context.Context) (string, error) {
	conn := b.connection()
	if conn == nil {
		return "", ErrNoEmulator
	}
	png, err := conn.Screenshot(ctx)
	if err != nil {
		return "", err
	}
	file, err := b.newCapture("Screenshot", conn.Serial(), ".png")
	if err != nil {
		return "", err
	}
	_, err = file.Write(png)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(file.Name())
		return "", err
	}
	b.saved(file.Name())

	return file.Name(), nil
}

// StartRecording starts the device's screenrecord; the recording is saved when it stops, whoever stops it.
func (b *Bridge) StartRecording(ctx context.Context) error {
	device, err := b.adbDevice()
	if err != nil {
		return err
	}
	b.mu.Lock()
	if b.recording != nil {
		b.mu.Unlock()
		return ErrRecording
	}
	current := &recording{
		device:     device,
		devicePath: fmt.Sprintf("/sdcard/claude-recording-%d.mp4", b.config.Now().UnixNano()),
		started:    make(chan struct{}),
		done:       make(chan struct{}),
	}
	b.recording = current
	b.state.RecordingSince = b.config.Now()
	b.state.RecordingError = ""
	b.mu.Unlock()
	// The shell prints its pid and becomes screenrecord, so a stop interrupts this recording and no other.
	recorder := device.Command("shell", "echo $$; exec screenrecord "+current.devicePath)
	var stderr strings.Builder
	recorder.Stderr = &stderr
	pid, err := startRecorder(ctx, recorder)
	if err != nil {
		close(current.started)
		b.endRecording(current, "", fmt.Errorf("cannot start screenrecord: %w: %s", err, strings.TrimSpace(stderr.String())))
		return current.err
	}
	current.pid = pid
	close(current.started)
	b.notifyViewers()
	go b.saveRecording(recorder, &stderr, current)

	return nil
}

// startRecorder starts the recorder and reads the device pid it prints first.
func startRecorder(ctx context.Context, recorder *exec.Cmd) (string, error) {
	stdout, err := recorder.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := recorder.Start(); err != nil {
		return "", err
	}
	line := make(chan string, 1)
	go func() {
		text, _ := bufio.NewReader(stdout).ReadString('\n')
		line <- strings.TrimSpace(text)
	}()
	select {
	case pid := <-line:
		if _, err := strconv.Atoi(pid); err == nil {
			return pid, nil
		}
		err = fmt.Errorf("the device answered %q instead of a pid", pid)
	case <-ctx.Done():
		err = ctx.Err()
	}
	recorder.Process.Kill()
	recorder.Wait()

	return "", err
}

// StopRecording interrupts the screen recording and returns the path it was saved to.
func (b *Bridge) StopRecording(ctx context.Context) (string, error) {
	b.mu.Lock()
	current := b.recording
	b.mu.Unlock()
	if current == nil {
		return "", ErrNotRecording
	}
	select {
	case <-current.started:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if current.pid != "" {
		// screenrecord finishes the MP4 on SIGINT, as on Ctrl-C; one already gone ended by itself and is being saved.
		interrupt := fmt.Sprintf("kill -INT %[1]s 2>/dev/null || ! kill -0 %[1]s 2>/dev/null", current.pid)
		if _, err := current.device.Run(ctx, "shell", interrupt); err != nil {
			return "", err
		}
	}
	select {
	case <-current.done:
		return current.saved, current.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (b *Bridge) saveRecording(recorder *exec.Cmd, stderr *strings.Builder, current *recording) {
	recordErr := recorder.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), recordingSaveTimeout)
	defer cancel()
	path, err := b.pullCapture(ctx, current.device, current.devicePath)
	current.device.Run(ctx, "shell", "rm", "-f", current.devicePath)
	if err != nil && recordErr != nil {
		err = fmt.Errorf("screenrecord failed: %w: %s", recordErr, strings.TrimSpace(stderr.String()))
	}
	if err != nil {
		b.config.Logger.Warn("cannot save the screen recording", "err", err)
	}
	b.endRecording(current, path, err)
}

func (b *Bridge) pullCapture(ctx context.Context, device adb.Device, devicePath string) (string, error) {
	file, err := b.newCapture("Screen_recording", device.Serial, ".mp4")
	if err != nil {
		return "", err
	}
	file.Close()
	if _, err := device.Run(ctx, "pull", devicePath, file.Name()); err != nil {
		os.Remove(file.Name())
		return "", err
	}

	return file.Name(), nil
}

func (b *Bridge) endRecording(current *recording, path string, err error) {
	b.mu.Lock()
	if b.recording == current {
		b.recording = nil
		b.state.RecordingSince = time.Time{}
		if err == nil {
			b.state.Saved = path
		} else {
			b.state.RecordingError = err.Error()
		}
	}
	b.mu.Unlock()
	current.saved, current.err = path, err
	close(current.done)
	b.notifyViewers()
}

// OpenAppSettings opens the system App info screen of the app in front.
func (b *Bridge) OpenAppSettings(ctx context.Context) error {
	device, err := b.adbDevice()
	if err != nil {
		return err
	}
	activities, err := device.Run(ctx, "shell", "dumpsys", "activity", "activities")
	if err != nil {
		return err
	}
	front := adb.ForegroundPackage(activities)
	home, err := device.Run(ctx, "shell", "cmd", "package", "resolve-activity", "--brief",
		"-a", "android.intent.action.MAIN", "-c", "android.intent.category.HOME")
	if err != nil {
		return err
	}
	if front == "" || front == adb.ResolvedPackage(home) {
		return ErrHomeInFront
	}
	_, err = device.Run(ctx, "shell", "am", "start", "-a", "android.settings.APPLICATION_DETAILS_SETTINGS", "-d", "package:"+front)

	return err
}

func (b *Bridge) connection() device.Device {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.conn
}

func (b *Bridge) adbDevice() (adb.Device, error) {
	conn := b.connection()
	if conn == nil {
		return adb.Device{}, ErrNoEmulator
	}
	if found, err := sdk.Find(b.config.SDKCandidates, runtime.GOOS); err == nil {
		if _, err := os.Stat(found.ADB()); err == nil {
			return adb.Device{Path: found.ADB(), Serial: conn.Serial()}, nil
		}
	}
	path, err := exec.LookPath("adb")
	if err != nil {
		return adb.Device{}, ErrNoADB
	}

	return adb.Device{Path: path, Serial: conn.Serial()}, nil
}

// newCapture creates <kind>_<serial>_<yyyyMMdd-HHmmss>.<ext> in the captures directory, numbering a name already taken.
func (b *Bridge) newCapture(kind, serial, ext string) (*os.File, error) {
	if err := os.MkdirAll(b.config.CapturesDir, 0o755); err != nil {
		return nil, err
	}
	base := filepath.Join(b.config.CapturesDir, fmt.Sprintf("%s_%s_%s", kind, serial, b.config.Now().Format("20060102-150405")))
	for number := 1; ; number++ {
		name := base + ext
		if number > 1 {
			name = fmt.Sprintf("%s_%d%s", base, number, ext)
		}
		file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if !errors.Is(err, fs.ErrExist) {
			return file, err
		}
	}
}

func (b *Bridge) saved(path string) {
	b.mu.Lock()
	b.state.Saved = path
	b.mu.Unlock()
	b.notifyViewers()
}
