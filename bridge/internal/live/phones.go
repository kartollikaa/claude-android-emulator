package live

import (
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/adb"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/device"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/discovery"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/phone"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/sdk"
)

const (
	listTimeout = 5 * time.Second
	// A phone whose stream keeps failing is tried again at doubling intervals, up to this many retry intervals apart.
	maxPhoneBackoff = 30
)

// phoneFailure is a phone whose stream failed before its first frame: how often in a row, why, and when to try again.
type phoneFailure struct {
	count   int
	err     string
	retryAt time.Time
}

var unsafeInFileName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// adbPath finds adb: the configured one, else the SDK's platform-tools, else PATH.
func (b *Bridge) adbPath() (string, error) {
	if b.config.ADB != "" {
		if _, err := os.Stat(b.config.ADB); err != nil {
			return "", ErrNoADB
		}
		return b.config.ADB, nil
	}
	if found, err := sdk.Find(b.config.SDKCandidates, runtime.GOOS); err == nil {
		if _, err := os.Stat(found.ADB()); err == nil {
			return found.ADB(), nil
		}
	}
	path, err := exec.LookPath("adb")
	if err != nil {
		return "", ErrNoADB
	}

	return path, nil
}

// connectPhone connects to the first phone adb lists that is not a discovered emulator, and notes one waiting for USB debugging.
func (b *Bridge) connectPhone(ctx context.Context) device.Device {
	path, err := b.adbPath()
	if err != nil {
		b.setWaiting("", "")
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "devices", "-l").Output()
	if err != nil {
		b.config.Logger.Debug("adb cannot list devices", "err", err)
		b.setWaiting("", "")
		return nil
	}
	emulators := map[string]bool{}
	if found, err := discovery.Find(b.config.DiscoveryDir); err == nil {
		for _, candidate := range found {
			emulators[candidate.Serial] = true
		}
	}
	listed := adb.ParseDevices(string(output))
	b.forgetUnlisted(listed)
	now := time.Now()
	choice := phone.Choose(listed, func(serial string) bool { return emulators[serial] || b.isBackingOff(serial, now) }, func(serial string) string {
		return b.serialNumber(ctx, adb.Device{Path: path, Serial: serial})
	})
	if !choice.IsFound {
		b.setWaiting(choice.Unauthorized, b.phoneError(listed, emulators))
		return nil
	}
	conn, err := phone.Dial(ctx, choice.Phone, phone.Options{ADB: path, Logger: b.config.Logger, CacheDir: b.config.LogDir})
	if err != nil {
		b.config.Logger.Debug("phone did not answer", "serial", choice.Phone.Serial, "err", err)
		b.noteFailure(choice.Phone.Serial, err)
		return nil
	}

	return conn
}

// serialNumber is the device's ro.serialno, the same over USB and Wi-Fi; adb's serial names the transport.
func (b *Bridge) serialNumber(ctx context.Context, shell adb.Device) string {
	b.mu.Lock()
	known, ok := b.serialNumbers[shell.Serial]
	b.mu.Unlock()
	if ok {
		return known
	}
	answer, err := shell.Run(ctx, "shell", "getprop", "ro.serialno")
	if err != nil {
		return ""
	}
	number := strings.TrimSpace(answer)
	b.mu.Lock()
	b.serialNumbers[shell.Serial] = number
	b.mu.Unlock()

	return number
}

// forgetUnlisted drops what the bridge remembers about serials adb no longer lists: a serial can come back as another device.
func (b *Bridge) forgetUnlisted(listed []adb.Listed) {
	b.mu.Lock()
	defer b.mu.Unlock()
	isListed := func(serial string) bool {
		return slices.ContainsFunc(listed, func(device adb.Listed) bool { return device.Serial == serial })
	}
	maps.DeleteFunc(b.serialNumbers, func(serial, _ string) bool { return !isListed(serial) })
	maps.DeleteFunc(b.phoneFailures, func(serial string, _ phoneFailure) bool { return !isListed(serial) })
}

func (b *Bridge) forgetFailures(serial string) {
	b.mu.Lock()
	delete(b.phoneFailures, serial)
	b.mu.Unlock()
}

// noteFailure counts one more failure of a phone that never streamed, puts off its next try and says why.
func (b *Bridge) noteFailure(serial string, err error) {
	if err == nil {
		err = errors.New("its stream ended before its first frame")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	failure := b.phoneFailures[serial]
	failure.count++
	failure.err = err.Error()
	delay := b.config.RetryInterval
	for range failure.count {
		delay = min(2*delay, b.config.RetryInterval*maxPhoneBackoff)
	}
	failure.retryAt = time.Now().Add(delay)
	b.phoneFailures[serial] = failure
}

func (b *Bridge) isBackingOff(serial string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	failure, ok := b.phoneFailures[serial]

	return ok && now.Before(failure.retryAt)
}

// phoneError names the first listed phone waiting out a failure, and why it failed.
func (b *Bridge) phoneError(listed []adb.Listed, emulators map[string]bool) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, device := range listed {
		if failure, ok := b.phoneFailures[device.Serial]; ok && device.State == "device" && !emulators[device.Serial] {
			return device.Serial + ": " + failure.err
		}
	}

	return ""
}

// setWaiting notes, while nothing shows, a phone waiting for USB debugging and a phone whose stream failed.
func (b *Bridge) setWaiting(unauthorized, phoneError string) {
	b.mu.Lock()
	isChange := b.state.Unauthorized != unauthorized || b.state.PhoneError != phoneError
	b.state.Unauthorized, b.state.PhoneError = unauthorized, phoneError
	b.mu.Unlock()
	if isChange {
		b.notifyViewers()
	}
}

// watchForEmulator ends the phone's stream once an emulator answers, so the bridge shows the emulator instead.
func (b *Bridge) watchForEmulator(ctx context.Context, stopPhone context.CancelFunc) {
	for {
		sleep(ctx, b.config.RetryInterval)
		if ctx.Err() != nil {
			return
		}
		if conn := b.connectEmulator(ctx); conn != nil {
			conn.Close()
			b.config.Logger.Info("an emulator came up; showing it instead of the phone", "serial", conn.Serial())
			stopPhone()
			return
		}
	}
}

// fileSafe makes a serial fit a file name: a Wi-Fi serial carries a host:port or an mDNS service name.
func fileSafe(serial string) string {
	return strings.Trim(unsafeInFileName.ReplaceAllString(serial, "-"), "-.")
}
