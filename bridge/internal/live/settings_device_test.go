//go:build unix

package live

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The fake device's cmd, settings and wm keep the configuration in files under state/.
var fakeDeviceCommands = map[string]string{
	"cmd": `#!/bin/sh
s="$(dirname "$0")/../state"
case "$1" in
uimode)
  [ -n "$3" ] && echo "$3" > "$s/night"
  echo "Night mode: $(cat "$s/night")" ;;
locale)
  [ -f "$s/no-locale" ] && { echo "Unknown command: $2"; exit 255; }
  case "$2" in
  get-device-locale) cat "$s/locale" ;;
  set-device-locale) echo "$3" > "$s/locale" ;;
  list-device-locales) printf 'ru-RU\nen-US\nfr-FR\n' ;;
  esac ;;
esac
`,
	"settings": `#!/bin/sh
s="$(dirname "$0")/../state"
case "$1" in
get) cat "$s/font" 2>/dev/null || echo null ;;
put) echo "$4" > "$s/font" ;;
esac
`,
	"wm": `#!/bin/sh
s="$(dirname "$0")/../state"
case "$2" in
"")
  echo "Physical density: $(cat "$s/physical")"
  if [ -f "$s/override" ]; then echo "Override density: $(cat "$s/override")"; fi ;;
reset) rm -f "$s/override" ;;
*) echo "$2" > "$s/override" ;;
esac
`,
}

// deviceWithSettings starts a bridge on a fake device that is light, at font scale 1 and 420 dpi, in en-US,
// with state the files to change first; it returns the harness and adb's folder.
func deviceWithSettings(t *testing.T, state map[string]string) (*harness, string) {
	t.Helper()
	root, tools := sdkWithADB(t)
	for _, dir := range []string{"device-bin", "state"} {
		if err := os.MkdirAll(filepath.Join(tools, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, script := range fakeDeviceCommands {
		if err := os.WriteFile(filepath.Join(tools, "device-bin", name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"night": "no", "physical": "420", "locale": "en-US"}
	for name, text := range state {
		files[name] = text
	}
	for name, text := range files {
		writeFile(t, filepath.Join(tools, "state", name), text+"\n")
	}
	h := startBridge(t, withSDK(root))
	h.connected(t, testContext(t), "")

	return h, tools
}

func deviceState(t *testing.T, tools, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tools, "state", name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func decodeSettings(t *testing.T, status int, answer string) Settings {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("answered %d %s, want 200", status, answer)
	}
	var settings Settings
	if err := json.Unmarshal([]byte(answer), &settings); err != nil {
		t.Fatalf("answer %q: %v", answer, err)
	}
	return settings
}

func changeSettings(t *testing.T, h *harness, body string) Settings {
	t.Helper()
	status, answer := control(t, h, "/settings", body)
	return decodeSettings(t, status, answer)
}

func readSettings(t *testing.T, h *harness) Settings {
	t.Helper()
	status, answer := send(t, h, http.MethodGet, "/settings", "", "", "")
	return decodeSettings(t, status, answer)
}

func settingsCalls(tools string) []string {
	var calls []string
	for _, call := range adbCalls(tools) {
		if !strings.Contains(call, `night=$(cmd uimode night)`) && call != "" {
			calls = append(calls, call)
		}
	}
	return calls
}

func TestSettingsReadTheCurrentConfiguration(t *testing.T) {
	h, _ := deviceWithSettings(t, map[string]string{"night": "yes", "font": "1.15", "override": "480", "locale": "ru-RU"})

	settings := readSettings(t, h)
	locale := "ru-RU"
	want := Settings{
		DarkTheme: true, FontScale: 1.15, FontScaleSteps: fontScaleSteps,
		Density: 480, PhysicalDensity: 420, DensitySteps: densitySteps(420, deviceWidth), Locale: &locale,
	}
	if !reflect.DeepEqual(settings, want) {
		t.Errorf("settings %+v, want %+v", settings, want)
	}
}

func TestDarkThemeSwitches(t *testing.T) {
	h, tools := deviceWithSettings(t, nil)

	if settings := changeSettings(t, h, `{"darkTheme": true}`); !settings.DarkTheme || deviceState(t, tools, "night") != "yes" {
		t.Errorf("after darkTheme true: answered %v, device night mode %q", settings.DarkTheme, deviceState(t, tools, "night"))
	}
	if settings := changeSettings(t, h, `{"darkTheme": false}`); settings.DarkTheme || deviceState(t, tools, "night") != "no" {
		t.Errorf("after darkTheme false: answered %v, device night mode %q", settings.DarkTheme, deviceState(t, tools, "night"))
	}
}

func TestFontScaleChanges(t *testing.T) {
	h, tools := deviceWithSettings(t, nil)

	if settings := changeSettings(t, h, `{"fontScale": 1.3}`); settings.FontScale != 1.3 {
		t.Errorf("answered font scale %v, want 1.3", settings.FontScale)
	}
	if calls := settingsCalls(tools); !reflect.DeepEqual(calls, []string{"-s " + serial + " shell settings put system font_scale 1.3"}) {
		t.Errorf("adb calls %q", calls)
	}
}

func TestFontScaleOutOfRange(t *testing.T) {
	h, tools := deviceWithSettings(t, nil)

	for _, body := range []string{`{"fontScale": 0.4}`, `{"fontScale": 3.1}`, `{"fontScale": -1}`, `{"fontScale": "big"}`} {
		if status, answer := control(t, h, "/settings", body); status != http.StatusBadRequest {
			t.Errorf("%s answered %d %s, want 400", body, status, answer)
		}
	}
	if calls := adbCalls(tools); len(calls) != 1 || calls[0] != "" {
		t.Errorf("refused changes ran adb: %q", calls)
	}
}

func TestDensityChangesAndResets(t *testing.T) {
	h, tools := deviceWithSettings(t, nil)

	if settings := changeSettings(t, h, `{"density": 480}`); settings.Density != 480 || settings.PhysicalDensity != 420 {
		t.Errorf("after density 480: density %d of physical %d", settings.Density, settings.PhysicalDensity)
	}
	if settings := changeSettings(t, h, `{"density": 0}`); settings.Density != 420 || deviceState(t, tools, "override") != "" {
		t.Errorf("after density 0: density %d, device override %q", settings.Density, deviceState(t, tools, "override"))
	}
}

func TestDensityOutOfRange(t *testing.T) {
	h, tools := deviceWithSettings(t, nil)

	for _, body := range []string{`{"density": 100}`, `{"density": 1000}`, `{"density": -480}`, `{"density": 480.5}`} {
		if status, answer := control(t, h, "/settings", body); status != http.StatusBadRequest {
			t.Errorf("%s answered %d %s, want 400", body, status, answer)
		}
	}
	if calls := adbCalls(tools); len(calls) != 1 || calls[0] != "" {
		t.Errorf("refused changes ran adb: %q", calls)
	}
}

func TestLocaleChanges(t *testing.T) {
	h, tools := deviceWithSettings(t, nil)

	if settings := changeSettings(t, h, `{"locale": "fr-FR"}`); settings.Locale == nil || *settings.Locale != "fr-FR" {
		t.Errorf("answered locale %v, want fr-FR", settings.Locale)
	}
	if locale := deviceState(t, tools, "locale"); locale != "fr-FR" {
		t.Errorf("device locale %q, want fr-FR", locale)
	}
}

func TestLocaleRejectsANonTag(t *testing.T) {
	h, tools := deviceWithSettings(t, nil)

	for _, tag := range []string{"ru_RU", "en-US; reboot", "$(reboot)", "", "e", "english"} {
		body, _ := json.Marshal(map[string]string{"locale": tag})
		if status, answer := control(t, h, "/settings", string(body)); status != http.StatusBadRequest {
			t.Errorf("locale %q answered %d %s, want 400", tag, status, answer)
		}
	}
	if calls := adbCalls(tools); len(calls) != 1 || calls[0] != "" {
		t.Errorf("refused changes ran adb: %q", calls)
	}
}

func TestLocaleUnavailableOnOlderDevices(t *testing.T) {
	h, tools := deviceWithSettings(t, map[string]string{"no-locale": ""})

	if settings := readSettings(t, h); settings.Locale != nil {
		t.Errorf("settings report locale %q on a device without the command", *settings.Locale)
	}
	if status, answer := control(t, h, "/settings", `{"darkTheme": true, "locale": "ru-RU"}`); status != http.StatusNotImplemented {
		t.Errorf("a locale change answered %d %s, want 501", status, answer)
	}
	if night := deviceState(t, tools, "night"); night != "no" {
		t.Errorf("the refused change still set night mode %q", night)
	}
	if status, answer := send(t, h, http.MethodGet, "/settings/locales", "", "", ""); status != http.StatusNotImplemented {
		t.Errorf("the locale list answered %d %s, want 501", status, answer)
	}
}

func TestLocalesListTheDevicesLocales(t *testing.T) {
	h, _ := deviceWithSettings(t, nil)

	status, answer := send(t, h, http.MethodGet, "/settings/locales", "", "", "")
	var list struct{ Locales []string }
	if err := json.Unmarshal([]byte(answer), &list); status != http.StatusOK || err != nil {
		t.Fatalf("answered %d %s", status, answer)
	}
	if want := []string{"en-US", "fr-FR", "ru-RU"}; !reflect.DeepEqual(list.Locales, want) {
		t.Errorf("locales %q, want %q", list.Locales, want)
	}
}

func TestEmptySettingsChangeIsRefused(t *testing.T) {
	h, _ := deviceWithSettings(t, nil)

	if status, answer := control(t, h, "/settings", `{}`); status != http.StatusBadRequest {
		t.Errorf("an empty change answered %d %s, want 400", status, answer)
	}
}

func TestResetRestoresTheDefaults(t *testing.T) {
	h, tools := deviceWithSettings(t, map[string]string{"night": "yes", "font": "1.5", "override": "500", "locale": "fr-FR"})

	status, answer := control(t, h, "/settings/reset", `{}`)
	settings := decodeSettings(t, status, answer)
	if settings.DarkTheme || settings.FontScale != 1 || settings.Density != 420 || settings.Locale == nil || *settings.Locale != "fr-FR" {
		t.Errorf("after reset %+v, want light, scale 1, 420 dpi and fr-FR kept", settings)
	}
	if override := deviceState(t, tools, "override"); override != "" {
		t.Errorf("the device still overrides its density with %s", override)
	}
}

func TestSettingsRoutesRefuseForeignNonJSONAndDisconnected(t *testing.T) {
	h, tools := deviceWithSettings(t, nil)

	for _, path := range []string{"/settings", "/settings/reset"} {
		if status, _ := send(t, h, http.MethodPost, path, "text/plain", "", `{"darkTheme": true}`); status != http.StatusUnsupportedMediaType {
			t.Errorf("%s as text/plain answered %d, want 415", path, status)
		}
		if status, _ := send(t, h, http.MethodPost, path, "application/json", "http://evil.example", `{"darkTheme": true}`); status != http.StatusForbidden {
			t.Errorf("%s from a foreign Origin answered %d, want 403", path, status)
		}
	}
	for _, path := range []string{"/settings", "/settings/locales"} {
		if status, _ := send(t, h, http.MethodGet, path, "", "http://evil.example", ""); status != http.StatusForbidden {
			t.Errorf("GET %s from a foreign Origin answered %d, want 403", path, status)
		}
	}
	if night := deviceState(t, tools, "night"); night != "no" {
		t.Errorf("refused requests set night mode %q", night)
	}

	alone := startBridge(t)
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/settings", ""},
		{http.MethodGet, "/settings/locales", ""},
		{http.MethodPost, "/settings", `{"darkTheme": true}`},
		{http.MethodPost, "/settings/reset", `{}`},
	} {
		if status, _ := send(t, alone, route.method, route.path, "application/json", "", route.body); status != http.StatusServiceUnavailable {
			t.Errorf("%s %s with no emulator answered %d, want 503", route.method, route.path, status)
		}
	}
}
