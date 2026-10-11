package live

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ErrLocaleUnavailable is returned for a language change on a device whose adb cannot set its locale.
var ErrLocaleUnavailable = errors.New("this Android version cannot change the device language through adb")

var (
	languageTag  = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$`)
	densityLines = regexp.MustCompile(`(?m)^(Physical|Override) density: ([0-9]+)\s*$`)
	sizeLines    = regexp.MustCompile(`(?m)^(?:Physical|Override) size: ([0-9]+)x([0-9]+)\s*$`)
)

// Font size's steps in Android's own Settings.
var fontScaleSteps = []float64{0.85, 1, 1.15, 1.3, 1.5, 1.8, 2}

const (
	minFontScale, maxFontScale = 0.5, 3.0
	minDensity, maxDensity     = 120, 960
)

const (
	// One shell call reads every setting, each on a line of its own; dumpsys tells whether a scheduled dark theme is on now.
	readSettingsScript = `echo "night=$(cmd uimode night)"; echo "computed=$(dumpsys uimode | grep -o 'mComputedNightMode=[a-z]*')"; ` +
		`echo "font=$(settings get system font_scale)"; wm density; wm size; echo "locale=$(cmd locale get-device-locale 2>/dev/null)"`
	// The shell's own exit stays 0, so an error is adb's and a device without the command answers something else.
	readLocaleScript = "cmd locale get-device-locale 2>/dev/null; true"
)

// Settings is the device configuration the settings panel shows and changes.
type Settings struct {
	DarkTheme       bool      `json:"darkTheme"`
	FontScale       float64   `json:"fontScale"`
	FontScaleSteps  []float64 `json:"fontScaleSteps"`
	Density         int       `json:"density"`
	PhysicalDensity int       `json:"physicalDensity"`
	DensitySteps    []int     `json:"densitySteps"`
	// Locale is the device's language tag, null where adb cannot set it.
	Locale *string `json:"locale"`
	// The display's current short side in pixels, which bounds the largest display size.
	shortSide int
}

// SettingsChange names the settings to change; the ones left out stay as they are.
type SettingsChange struct {
	DarkTheme *bool    `json:"darkTheme"`
	FontScale *float64 `json:"fontScale"`
	// Density 0 returns the display to its physical density.
	Density *int    `json:"density"`
	Locale  *string `json:"locale"`
}

// ReadSettings reads the device's configuration through adb.
func (b *Bridge) ReadSettings(ctx context.Context) (Settings, error) {
	device, err := b.adbDevice()
	if err != nil {
		return Settings{}, err
	}
	answer, err := device.Run(ctx, "shell", readSettingsScript)
	if err != nil {
		return Settings{}, err
	}
	return parseSettings(answer)
}

// DeviceLocales lists the language tags the device can switch to.
func (b *Bridge) DeviceLocales(ctx context.Context) ([]string, error) {
	device, err := b.adbDevice()
	if err != nil {
		return nil, err
	}
	// As in readLocaleScript, a device without the command answers no tags.
	answer, err := device.Run(ctx, "shell", "cmd locale list-device-locales 2>/dev/null; true")
	if err != nil {
		return nil, err
	}
	tags := strings.Fields(answer)
	if len(tags) == 0 || !languageTag.MatchString(tags[0]) {
		return nil, ErrLocaleUnavailable
	}
	slices.Sort(tags)

	return tags, nil
}

// ChangeSettings applies the change once every value in it is valid and answers the configuration read back.
func (b *Bridge) ChangeSettings(ctx context.Context, change SettingsChange) (Settings, error) {
	if err := change.validate(); err != nil {
		return Settings{}, err
	}
	device, err := b.adbDevice()
	if err != nil {
		return Settings{}, err
	}
	if change.Locale != nil {
		current, err := device.Run(ctx, "shell", readLocaleScript)
		if err != nil {
			return Settings{}, err
		}
		if !languageTag.MatchString(strings.TrimSpace(current)) {
			return Settings{}, ErrLocaleUnavailable
		}
	}
	// One shell line stops at the first command that fails; every value in it was validated above.
	if _, err := device.Run(ctx, "shell", strings.Join(change.commands(), " && ")); err != nil {
		return Settings{}, err
	}

	return b.ReadSettings(ctx)
}

// ResetSettings turns the dark theme off and returns font and display size to their defaults; the language stays.
func (b *Bridge) ResetSettings(ctx context.Context) (Settings, error) {
	isDark, scale, density := false, 1.0, 0

	return b.ChangeSettings(ctx, SettingsChange{DarkTheme: &isDark, FontScale: &scale, Density: &density})
}

func (change SettingsChange) validate() error {
	switch {
	case change.DarkTheme == nil && change.FontScale == nil && change.Density == nil && change.Locale == nil:
		return fmt.Errorf("%w: name a setting to change: darkTheme, fontScale, density or locale", ErrBadInput)
	case change.FontScale != nil && !(*change.FontScale >= minFontScale && *change.FontScale <= maxFontScale):
		return fmt.Errorf("%w: fontScale %v is outside %v–%v", ErrBadInput, *change.FontScale, minFontScale, maxFontScale)
	case change.Density != nil && *change.Density != 0 && (*change.Density < minDensity || *change.Density > maxDensity):
		return fmt.Errorf("%w: density %d is outside %d–%d (0 resets it)", ErrBadInput, *change.Density, minDensity, maxDensity)
	case change.Locale != nil && !languageTag.MatchString(*change.Locale):
		return fmt.Errorf("%w: locale %q is not a BCP 47 language tag", ErrBadInput, *change.Locale)
	}

	return nil
}

func (change SettingsChange) commands() []string {
	var commands []string
	if change.DarkTheme != nil {
		mode := "no"
		if *change.DarkTheme {
			mode = "yes"
		}
		commands = append(commands, "cmd uimode night "+mode)
	}
	if change.FontScale != nil {
		commands = append(commands, "settings put system font_scale "+strconv.FormatFloat(*change.FontScale, 'f', -1, 64))
	}
	if change.Density != nil {
		density := "reset"
		if *change.Density != 0 {
			density = strconv.Itoa(*change.Density)
		}
		commands = append(commands, "wm density "+density)
	}
	if change.Locale != nil {
		commands = append(commands, "cmd locale set-device-locale "+*change.Locale)
	}

	return commands
}

func parseSettings(answer string) (Settings, error) {
	settings := Settings{FontScale: 1, FontScaleSteps: slices.Clone(fontScaleSteps)}
	computedNight := ""
	for _, line := range strings.Split(answer, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "night":
			settings.DarkTheme = strings.TrimSpace(strings.TrimPrefix(value, "Night mode:")) == "yes"
		case "computed":
			_, computedNight, _ = strings.Cut(value, "=")
		case "font":
			if scale, err := strconv.ParseFloat(value, 64); err == nil {
				settings.FontScale = scale
			}
		case "locale":
			if languageTag.MatchString(value) {
				settings.Locale = &value
			}
		}
	}
	for _, match := range densityLines.FindAllStringSubmatch(answer, -1) {
		density, _ := strconv.Atoi(match[2])
		if match[1] == "Physical" {
			settings.PhysicalDensity = density
		}
		settings.Density = density
	}
	if computedNight != "" {
		settings.DarkTheme = computedNight == "true"
	}
	for _, match := range sizeLines.FindAllStringSubmatch(answer, -1) {
		width, _ := strconv.Atoi(match[1])
		height, _ := strconv.Atoi(match[2])
		settings.shortSide = min(width, height)
	}
	if settings.PhysicalDensity == 0 || settings.shortSide == 0 {
		return Settings{}, fmt.Errorf("the device reported no display density or size: %q", strings.TrimSpace(answer))
	}
	settings.DensitySteps = densitySteps(settings.PhysicalDensity, settings.shortSide)

	return settings, nil
}

// densitySteps are Display size's choices in Android's Settings: one smaller, the default, up to three larger.
func densitySteps(physical, shortSidePixels int) []int {
	maxScale, minScale, minInterval := float32(1.5), float32(0.85), float32(0.09)
	// The largest keeps the screen at least 320 dp across.
	largest := min(maxScale, float32(160*shortSidePixels/320)/float32(physical))
	smaller := int(min(max((1-minScale)/minInterval, 0), 1))
	larger := int(min(max((largest-1)/minInterval, 0), 3))
	// float32, each product converted so it is not fused, and the dropped low bit repeat DisplayDensityUtils' arithmetic.
	at := func(scale float32) int { return int(float32(physical)*scale) &^ 1 }
	var steps []int
	for i := smaller; i >= 1; i-- {
		steps = append(steps, at(1-float32(float32(i)*((1-minScale)/float32(smaller)))))
	}
	steps = append(steps, physical)
	for i := 1; i <= larger; i++ {
		steps = append(steps, at(1+float32(float32(i)*((largest-1)/float32(larger)))))
	}

	return steps
}
