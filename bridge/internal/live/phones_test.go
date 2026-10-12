package live

import (
	"maps"
	"slices"
	"testing"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/adb"
)

func TestSerialNumbersOfUnlistedDevicesAreForgotten(t *testing.T) {
	b := New(Config{})
	b.serialNumbers = map[string]string{"192.168.1.20:5555": "A", "5C111JEBF10513": "A", "10.0.0.7:5555": "B"}

	b.forgetUnlisted([]adb.Listed{{Serial: "5C111JEBF10513", State: "device"}, {Serial: "10.0.0.7:5555", State: "offline"}})

	if kept := slices.Sorted(maps.Keys(b.serialNumbers)); !slices.Equal(kept, []string{"10.0.0.7:5555", "5C111JEBF10513"}) {
		t.Errorf("kept serial numbers of %v, want only the listed devices'", kept)
	}
}
