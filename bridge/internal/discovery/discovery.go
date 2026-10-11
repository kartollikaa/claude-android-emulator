// Package discovery finds running Android emulators from the files they write on start.
package discovery

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/ini"
)

// Emulator is one running emulator that serves gRPC; AVD is the name it was started with.
type Emulator struct {
	Serial   string
	AVD      string
	GRPCPort int
	Token    string
}

var pidFile = regexp.MustCompile(`^pid_(\d+)\.ini$`)

type running struct {
	pid    int
	values map[string]string
}

// Find lists the emulators in dir that expose a gRPC port, in file-name order; a missing dir means none run.
func Find(dir string) ([]Emulator, error) {
	all, err := readAll(dir)
	if err != nil {
		return nil, err
	}
	var found []Emulator
	for _, file := range all {
		values := file.values
		port, err := strconv.Atoi(values["grpc.port"])
		if err != nil || port <= 0 {
			continue
		}
		emulator := Emulator{AVD: values["avd.id"], GRPCPort: port, Token: values["grpc.token"]}
		if serial := values["port.serial"]; serial != "" {
			emulator.Serial = "emulator-" + serial
		}
		found = append(found, emulator)
	}

	return found, nil
}

// RunningAVDs names the AVDs of every emulator in dir whose process is alive, gRPC or not.
func RunningAVDs(dir string) (map[string]bool, error) {
	all, err := readAll(dir)
	if err != nil {
		return nil, err
	}
	avds := map[string]bool{}
	for _, file := range all {
		// A killed emulator leaves its file behind.
		if avd := file.values["avd.id"]; avd != "" && isAlive(file.pid) {
			avds[avd] = true
		}
	}

	return avds, nil
}

func readAll(dir string) ([]running, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if pidFile.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	var all []running
	for _, name := range names {
		// An emulator shutting down removes its file between the listing and the read.
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		pid, _ := strconv.Atoi(pidFile.FindStringSubmatch(name)[1])
		all = append(all, running{pid: pid, values: ini.Parse(string(data))})
	}

	return all, nil
}

// DefaultDir is where the emulator writes its pid_<pid>.ini on this platform.
func DefaultDir() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Caches", "TemporaryItems", "avd", "running")
	case "windows":
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "Temp", "avd", "running")
	default:
		if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
			return filepath.Join(runtimeDir, "avd", "running")
		}
		return filepath.Join("/run", "user", strconv.Itoa(os.Getuid()), "avd", "running")
	}
}
