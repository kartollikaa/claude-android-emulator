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
	"strings"
)

// Emulator is one running emulator that serves gRPC.
type Emulator struct {
	Serial   string
	GRPCPort int
	Token    string
}

var pidFile = regexp.MustCompile(`^pid_\d+\.ini$`)

// Find lists the emulators in dir that expose a gRPC port, in file-name order; a missing dir means none run.
func Find(dir string) ([]Emulator, error) {
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

	var found []Emulator
	for _, name := range names {
		// An emulator shutting down removes its file between the listing and the read.
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		values := parseINI(string(data))
		port, err := strconv.Atoi(values["grpc.port"])
		if err != nil || port <= 0 {
			continue
		}
		emulator := Emulator{GRPCPort: port, Token: values["grpc.token"]}
		if serial := values["port.serial"]; serial != "" {
			emulator.Serial = "emulator-" + serial
		}
		found = append(found, emulator)
	}

	return found, nil
}

func parseINI(text string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if ok && key != "" {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}

	return values
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
