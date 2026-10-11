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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/ini"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/proc"
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

const (
	// Longer than the emulator's own wait for its guest to shut down.
	stopGrace = 30 * time.Second
	// Set to the log path, it makes a run of this binary the emulator launcher.
	launcherLog = "ANDROID_BRIDGE_LAUNCHER_LOG"
)

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

// Boot starts the emulator for avd with its output in logPath, through a launcher that exits at once:
// the emulator then belongs to no process tree of the bridge's, so whatever kills that tree spares it.
func (s SDK) Boot(avd string, grpcPort int, logPath string) (pid int, err error) {
	if !avdName.MatchString(avd) {
		return 0, errors.New("not an AVD name: " + avd)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	launcher := exec.Command(self, s.Emulator(), "-avd", avd, "-qt-hide-window", "-gpu", "host",
		"-grpc", fmt.Sprint(grpcPort), "-grpc-use-token")
	launcher.Env = append(os.Environ(), launcherLog+"="+logPath)
	var stderr strings.Builder
	launcher.Stderr = &stderr
	output, err := launcher.Output()
	if err != nil {
		return 0, fmt.Errorf("launching the emulator: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0, fmt.Errorf("the emulator launcher answered %q", output)
	}

	return pid, nil
}

// RunLauncherIfAsked makes this process the launcher Boot starts: it starts the emulator, prints its pid and exits.
func RunLauncherIfAsked() {
	logPath := os.Getenv(launcherLog)
	if logPath == "" {
		return
	}
	pid, err := launch(os.Args[1:], logPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(pid)
	os.Exit(0)
}

func launch(argv []string, logPath string) (int, error) {
	log, err := os.Create(logPath)
	if err != nil {
		return 0, err
	}
	defer log.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = slices.DeleteFunc(os.Environ(), func(variable string) bool {
		return strings.HasPrefix(variable, launcherLog+"=")
	})
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		return 0, err
	}

	return cmd.Process.Pid, nil
}

// Stop asks a booted emulator to shut down and kills it if it is still there after stopGrace.
func Stop(pid int) {
	emulator, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if err := emulator.Signal(syscall.SIGTERM); err != nil {
		emulator.Kill()
		return
	}
	time.AfterFunc(stopGrace, func() {
		if proc.Alive(pid) {
			emulator.Kill()
		}
	})
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
