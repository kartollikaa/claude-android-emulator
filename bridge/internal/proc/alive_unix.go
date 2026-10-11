//go:build unix

// Package proc tells whether a process is still running.
package proc

import (
	"errors"
	"syscall"
)

// Alive reports whether the process with pid runs; one that exited but was not reaped yet does not.
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}

	return !isZombie(pid)
}
