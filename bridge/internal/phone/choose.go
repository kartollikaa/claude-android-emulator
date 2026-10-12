package phone

import (
	"strings"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/adb"
)

// Choice is the phone to show, if any, and a phone that waits for USB debugging to be allowed.
type Choice struct {
	Phone        adb.Listed
	IsFound      bool
	Unauthorized string
}

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
	if len(candidates) == 1 || choice.Phone.IsUSB {
		return choice
	}
	first := serialNumber(choice.Phone.Serial)
	if first == "" {
		return choice
	}
	for _, other := range candidates[1:] {
		if other.IsUSB && serialNumber(other.Serial) == first {
			choice.Phone = other
			break
		}
	}

	return choice
}

// IsWiFi reports whether adb reaches the device over the network: not over USB and not a local emulator.
func IsWiFi(device adb.Listed) bool {
	return !device.IsUSB && !strings.HasPrefix(device.Serial, "emulator-")
}
