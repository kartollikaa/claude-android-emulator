//go:build unix

// Package proc tells whether a process is still running.
package proc

import (
	"errors"
	"syscall"
)

// Alive reports whether the process with pid runs.
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
