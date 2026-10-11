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
	settings, err := parseSettings("night=Night mode: yes\nfont=1.15\nPhysical density: 420\nOverride density: 480\nlocale=ru-RU\n")
	if err != nil {
		t.Fatal(err)
	}
	if !settings.DarkTheme || settings.FontScale != 1.15 || settings.Density != 480 || settings.PhysicalDensity != 420 || settings.Locale == nil || *settings.Locale != "ru-RU" {
		t.Errorf("parsed %+v", settings)
	}

	settings, err = parseSettings("night=Night mode: auto\nfont=null\nPhysical density: 420\nlocale=Unknown command: get-device-locale\n")
	if err != nil {
		t.Fatal(err)
	}
	if settings.DarkTheme || settings.FontScale != 1 || settings.Density != 420 || settings.Locale != nil {
		t.Errorf("parsed %+v, want light, scale 1, the physical density and no locale", settings)
	}

	if _, err := parseSettings("night=Night mode: no\nfont=1.0\nlocale=en-US\n"); err == nil {
		t.Error("an answer without a density parsed")
	}
}
