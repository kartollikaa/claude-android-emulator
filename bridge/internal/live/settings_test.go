package live

import (
	"reflect"
	"testing"
)

func TestDensityStepsFollowAndroidSettings(t *testing.T) {
	for _, screen := range []struct {
		physical, shortSide int
		want                []int
	}{
		{420, 1080, []int{356, 420, 460, 500, 540}},
		{560, 1440, []int{476, 560, 612, 666, 720}},
		{240, 480, []int{204, 240}},
		{160, 320, []int{136, 160}},
	} {
		if got := densitySteps(screen.physical, screen.shortSide); !reflect.DeepEqual(got, screen.want) {
			t.Errorf("%d dpi, %d px across: steps %v, want %v", screen.physical, screen.shortSide, got, screen.want)
		}
	}
}

func TestParseSettings(t *testing.T) {
	settings, err := parseSettings("night=Night mode: yes\ncomputed=\nfont=1.15\nPhysical density: 420\nOverride density: 480\nPhysical size: 1080x2400\nlocale=ru-RU\n")
	if err != nil {
		t.Fatal(err)
	}
	if !settings.DarkTheme || settings.FontScale != 1.15 || settings.Density != 480 || settings.PhysicalDensity != 420 || settings.Locale == nil || *settings.Locale != "ru-RU" {
		t.Errorf("parsed %+v", settings)
	}

	settings, err = parseSettings("night=Night mode: auto\ncomputed=mComputedNightMode=false\nfont=null\nPhysical density: 420\nPhysical size: 1080x2400\nOverride size: 720x1600\nlocale=Unknown command: get-device-locale\n")
	if err != nil {
		t.Fatal(err)
	}
	if settings.DarkTheme || settings.FontScale != 1 || settings.Density != 420 || settings.Locale != nil || settings.shortSide != 720 {
		t.Errorf("parsed %+v, want light, scale 1, the physical density, no locale and the overridden size", settings)
	}

	settings.FontScaleSteps[0] = 9
	if again, _ := parseSettings("font=1\nPhysical density: 420\nPhysical size: 1080x2400\n"); again.FontScaleSteps[0] != 0.85 {
		t.Errorf("editing one answer's font steps changed the next answer's to %v", again.FontScaleSteps)
	}

	if _, err := parseSettings("night=Night mode: no\nfont=1.0\nPhysical size: 1080x2400\nlocale=en-US\n"); err == nil {
		t.Error("an answer without a density parsed")
	}
	if _, err := parseSettings("night=Night mode: no\nfont=1.0\nPhysical density: 420\nlocale=en-US\n"); err == nil {
		t.Error("an answer without a display size parsed")
	}
}
