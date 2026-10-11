//go:build unix

package discovery

import (
	"errors"
	"syscall"
)

func isAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
