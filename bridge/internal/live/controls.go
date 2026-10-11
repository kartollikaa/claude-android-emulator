package live

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/adb"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/emulator"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/sdk"
)

// The device's screenrecord stops by itself at its own time limit; pulling the file takes a moment more.
const recordingSaveTimeout = 30 * time.Second

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
	devicePath string
	done       chan struct{}
	saved      string
	err        error
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
	file, err := b.newCapture("Screenshot", conn.Emulator.Serial, ".png")
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
		devicePath: fmt.Sprintf("/sdcard/claude-recording-%d.mp4", b.config.Now().UnixNano()),
		done:       make(chan struct{}),
	}
	b.recording = current
	b.state.RecordingSince = b.config.Now()
	b.mu.Unlock()
	recorder := device.Command("shell", "screenrecord", current.devicePath)
	if err := recorder.Start(); err != nil {
		b.endRecording(current, "", err)
		return err
	}
	b.notifyViewers()
	go b.saveRecording(device, recorder, current)

	return nil
}

// StopRecording interrupts the screen recording and returns the path it was saved to.
func (b *Bridge) StopRecording(ctx context.Context) (string, error) {
	b.mu.Lock()
	current := b.recording
	b.mu.Unlock()
	if current == nil {
		return "", ErrNotRecording
	}
	device, err := b.adbDevice()
	if err != nil {
		return "", err
	}
	// screenrecord finishes the MP4 on SIGINT, as on Ctrl-C.
	if _, err := device.Run(ctx, "shell", "kill -2 $(pidof screenrecord)"); err != nil {
		return "", err
	}
	select {
	case <-current.done:
		return current.saved, current.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (b *Bridge) saveRecording(device adb.Device, recorder *exec.Cmd, current *recording) {
	recorder.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), recordingSaveTimeout)
	defer cancel()
	path, err := b.pullCapture(ctx, device, current.devicePath)
	device.Run(ctx, "shell", "rm", "-f", current.devicePath)
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

func (b *Bridge) connection() *emulator.Conn {
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
			return adb.Device{Path: found.ADB(), Serial: conn.Emulator.Serial}, nil
		}
	}
	path, err := exec.LookPath("adb")
	if err != nil {
		return adb.Device{}, ErrNoADB
	}

	return adb.Device{Path: path, Serial: conn.Emulator.Serial}, nil
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
