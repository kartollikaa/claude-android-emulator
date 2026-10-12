package phone

import (
	"regexp"
	"strings"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/adb"
)

// Choice is the phone to show, if any, and a phone that waits for USB debugging to be allowed.
type Choice struct {
	Phone        adb.Listed
	IsFound      bool
	Unauthorized string
}

// networkSerial is a serial adb gives a device it reaches over the network: host:port from adb connect, or an mDNS
// service name from wireless debugging.
var networkSerial = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[^:\s]+):[0-9]+$|\._adb-tls-connect\._tcp\.?$|\._adb\._tcp\.?$`)

// Choose picks the first phone adb lists in the device state that skip leaves; a phone listed twice, over USB
// and over Wi-Fi (the same serialNumber), shows over USB.
func Choose(listed []adb.Listed, skip func(serial string) bool, serialNumber func(serial string) string) Choice {
	var choice Choice
	var candidates []adb.Listed
	for _, device := range listed {
		if skip(device.Serial) {
			continue
		}
		switch device.State {
		case "device":
			candidates = append(candidates, device)
		case "unauthorized":
			if choice.Unauthorized == "" {
				choice.Unauthorized = device.Serial
			}
		}
	}
	if len(candidates) == 0 {
		return choice
	}
	choice.Phone, choice.IsFound = candidates[0], true
	if len(candidates) == 1 || !IsWiFi(choice.Phone) {
		return choice
	}
	first := serialNumber(choice.Phone.Serial)
	if first == "" {
		return choice
	}
	for _, other := range candidates[1:] {
		if !IsWiFi(other) && serialNumber(other.Serial) == first {
			choice.Phone = other
			break
		}
	}

	return choice
}

// IsWiFi reports whether adb reaches the device over the network, as its serial's shape tells: adb lists no usb:
// field on some platforms, and an emulator's serial names a console port, not a network address.
func IsWiFi(device adb.Listed) bool {
	return networkSerial.MatchString(device.Serial) && !strings.HasPrefix(device.Serial, "emulator-")
}
