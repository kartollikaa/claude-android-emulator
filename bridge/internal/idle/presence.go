// Package idle shuts down an emulator the mod booted once no bridge has shown it for a while.
//
// A bridge showing an emulator keeps a presence mark naming the AVD and its own pid; a watcher, started detached when
// the bridge boots the emulator, shuts it down after the idle period passes with no live mark.
package idle

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/proc"
)

const (
	// RefreshInterval is how often a bridge rewrites its mark.
	RefreshInterval = 30 * time.Second
	// A mark not rewritten for this long counts as absent, even if its pid now names another process.
	staleAfter = 3 * RefreshInterval
	markSuffix = ".presence"
)

// PresenceDir is where marks live under the bridge's log folder.
func PresenceDir(logDir string) string { return filepath.Join(logDir, "presence") }

// Mark is one bridge's claim that it shows avd; Period is that bridge's idle period.
type Mark struct {
	Dir    string
	AVD    string
	PID    int
	Period time.Duration
}

func (m Mark) path() string {
	return filepath.Join(m.Dir, m.AVD+"."+strconv.Itoa(m.PID)+markSuffix)
}

// Write creates or refreshes the mark.
func (m Mark) Write() error {
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return err
	}
	temporary := m.path() + ".tmp"
	if err := os.WriteFile(temporary, []byte(m.Period.String()), 0o600); err != nil {
		return err
	}

	return os.Rename(temporary, m.path())
}

// Remove deletes the mark; a missing one is fine.
func (m Mark) Remove() error {
	err := os.Remove(m.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return err
}

// Presence is what the live marks for one AVD say.
type Presence struct {
	IsShown bool
	// Period is the idle period of the most recently written live mark.
	Period time.Duration
}

// Look reads the marks in dir for avd at now; a mark whose bridge is gone or that went stale counts as absent.
func Look(dir, avd string, now time.Time) Presence {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Presence{}
	}
	var presence Presence
	var newest time.Time
	for _, entry := range entries {
		rest, isMark := strings.CutSuffix(entry.Name(), markSuffix)
		dot := strings.LastIndexByte(rest, '.')
		if !isMark || dot < 0 || rest[:dot] != avd {
			continue
		}
		pid, err := strconv.Atoi(rest[dot+1:])
		if err != nil || !proc.Alive(pid) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) > staleAfter {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		period, err := time.ParseDuration(strings.TrimSpace(string(data)))
		if err != nil {
			continue
		}
		presence.IsShown = true
		if info.ModTime().After(newest) {
			newest, presence.Period = info.ModTime(), period
		}
	}

	return presence
}
