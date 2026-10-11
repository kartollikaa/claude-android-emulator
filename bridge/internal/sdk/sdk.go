// Package sdk finds the Android SDK, lists the virtual devices it can boot and boots one.
package sdk

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/ini"
)

// SDK is an Android SDK folder that holds the emulator.
type SDK struct {
	Root string
	goos string
}

// AVD is one virtual device the SDK can boot.
type AVD struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	API         string `json:"api"`
}

// NotFoundError lists the folders that were searched for an SDK.
type NotFoundError struct{ Looked []string }

func (e *NotFoundError) Error() string {
	return "no Android SDK with an emulator in " + strings.Join(e.Looked, ", ")
}

// The emulator itself waits 20 s for the guest to shut down before it kills it.
const stopGrace = 30 * time.Second

var (
	avdName  = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]*$`)
	apiLevel = regexp.MustCompile(`^android-(\d+(?:\.\d+)?)$`)
)

// Candidates lists where the SDK may live, the environment first, then the platform default.
func Candidates(getenv func(string) string, home, goos string) []string {
	var dirs []string
	for _, name := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if dir := getenv(name); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	switch goos {
	case "darwin":
		dirs = append(dirs, filepath.Join(home, "Library", "Android", "sdk"))
	case "windows":
		if local := getenv("LOCALAPPDATA"); local != "" {
			dirs = append(dirs, filepath.Join(local, "Android", "Sdk"))
		}
	default:
		dirs = append(dirs, filepath.Join(home, "Android", "Sdk"))
	}

	return dirs
}

// Find returns the first candidate that holds the emulator binary.
func Find(candidates []string, goos string) (SDK, error) {
	for _, dir := range candidates {
		sdk := SDK{Root: dir, goos: goos}
		if info, err := os.Stat(sdk.Emulator()); err == nil && !info.IsDir() {
			return sdk, nil
		}
	}

	return SDK{}, &NotFoundError{Looked: candidates}
}

// Emulator is the path of the emulator binary.
func (s SDK) Emulator() string {
	if s.goos == "windows" {
		return filepath.Join(s.Root, "emulator", "emulator.exe")
	}
	return filepath.Join(s.Root, "emulator", "emulator")
}

// AVDHome is where AVDs keep their .ini files.
func AVDHome(getenv func(string) string, home string) string {
	if dir := getenv("ANDROID_AVD_HOME"); dir != "" {
		return dir
	}
	if dir := getenv("ANDROID_USER_HOME"); dir != "" {
		return filepath.Join(dir, "avd")
	}
	return filepath.Join(home, ".android", "avd")
}

// AVDs lists the virtual devices in the order `emulator -list-avds` prints them.
func (s SDK) AVDs(ctx context.Context, avdHome string) ([]AVD, error) {
	output, err := exec.CommandContext(ctx, s.Emulator(), "-list-avds").Output()
	if err != nil {
		return nil, fmt.Errorf("emulator -list-avds: %w", err)
	}
	avds := []AVD{}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		// Some emulator versions print log lines among the names.
		if !avdName.MatchString(name) {
			continue
		}
		avds = append(avds, describe(name, avdHome))
	}

	return avds, nil
}

func describe(name, avdHome string) AVD {
	avd := AVD{Name: name, DisplayName: name}
	dir := filepath.Join(avdHome, name+".avd")
	if pointer, err := os.ReadFile(filepath.Join(avdHome, name+".ini")); err == nil {
		if path := ini.Parse(string(pointer))["path"]; path != "" {
			dir = path
		}
	}
	config, err := os.ReadFile(filepath.Join(dir, "config.ini"))
	if err != nil {
		return avd
	}
	values := ini.Parse(string(config))
	if displayName := values["avd.ini.displayname"]; displayName != "" {
		avd.DisplayName = displayName
	}
	if match := apiLevel.FindStringSubmatch(values["target"]); match != nil {
		avd.API = match[1]
	}

	return avd
}

// Boot starts the emulator for avd, detached from this process, with its output in logPath.
func (s SDK) Boot(avd string, grpcPort int, logPath string) (*exec.Cmd, error) {
	if !avdName.MatchString(avd) {
		return nil, errors.New("not an AVD name: " + avd)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, err
	}
	log, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	cmd := exec.Command(s.Emulator(), "-avd", avd, "-qt-hide-window", "-gpu", "host",
		"-grpc", fmt.Sprint(grpcPort), "-grpc-use-token")
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return cmd, nil
}

// Stop asks a booted emulator to shut down and kills it if it is still there after stopGrace.
func Stop(cmd *exec.Cmd) {
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		cmd.Process.Kill()
		return
	}
	time.AfterFunc(stopGrace, func() { cmd.Process.Kill() })
}

// LastError picks the line of an emulator log that explains a failed boot.
func LastError(logPath string) string {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return "the emulator exited"
	}
	var last, lastError string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		last = line
		if strings.Contains(line, "ERROR") || strings.Contains(line, "FATAL") {
			lastError = line
		}
	}
	if lastError != "" {
		return lastError
	}
	if last != "" {
		return last
	}
	return "the emulator exited without output"
}
