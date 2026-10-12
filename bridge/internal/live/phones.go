package live

import (
	"context"
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

const listTimeout = 5 * time.Second

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
		b.setUnauthorized("")
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "devices", "-l").Output()
	if err != nil {
		b.config.Logger.Debug("adb cannot list devices", "err", err)
		b.setUnauthorized("")
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
	choice := phone.Choose(listed, func(serial string) bool { return emulators[serial] }, func(serial string) string {
		return b.serialNumber(ctx, adb.Device{Path: path, Serial: serial})
	})
	if !choice.IsFound {
		b.setUnauthorized(choice.Unauthorized)
		return nil
	}
	conn, err := phone.Dial(ctx, choice.Phone, phone.Options{ADB: path, Logger: b.config.Logger, CacheDir: b.config.LogDir})
	if err != nil {
		b.config.Logger.Debug("phone did not answer", "serial", choice.Phone.Serial, "err", err)
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
	for serial := range b.serialNumbers {
		if !slices.ContainsFunc(listed, func(device adb.Listed) bool { return device.Serial == serial }) {
			delete(b.serialNumbers, serial)
		}
	}
}

func (b *Bridge) setUnauthorized(serial string) {
	b.mu.Lock()
	isChange := b.state.Unauthorized != serial
	b.state.Unauthorized = serial
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
