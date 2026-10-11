// Package adb runs adb commands against one device and reads what they answer.
package adb

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Device is one device as adb addresses it: the adb binary and the device's serial.
type Device struct {
	Path   string
	Serial string
}

var (
	resumedActivity = regexp.MustCompile(`(?:topResumedActivity|mResumedActivity)[=:] ?ActivityRecord\{\S+ \S+ ([^/\s]+)/`)
	packageName     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)+$`)
)

// Command builds an adb command for the device without running it.
func (d Device) Command(args ...string) *exec.Cmd {
	return exec.Command(d.Path, append([]string{"-s", d.Serial}, args...)...)
}

// Run runs an adb command for the device and returns its standard output.
func (d Device) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, d.Path, append([]string{"-s", d.Serial}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("adb %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}

	return string(output), nil
}

// ForegroundPackage picks the package of the resumed activity from `dumpsys activity activities`.
func ForegroundPackage(activities string) string {
	match := resumedActivity.FindStringSubmatch(activities)
	if match == nil || !packageName.MatchString(match[1]) {
		return ""
	}

	return match[1]
}

// ResolvedPackage picks the package from `cmd package resolve-activity --brief`, whose last line is package/activity.
func ResolvedPackage(resolved string) string {
	lines := strings.Split(strings.TrimSpace(resolved), "\n")
	name, _, isComponent := strings.Cut(strings.TrimSpace(lines[len(lines)-1]), "/")
	if !isComponent || !packageName.MatchString(name) {
		return ""
	}

	return name
}
