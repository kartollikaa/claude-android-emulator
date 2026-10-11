package proc

import "golang.org/x/sys/unix"

// The kernel's process state for a zombie (SZOMB in sys/proc.h).
const zombieState = 5

func isZombie(pid int) bool {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)

	return err == nil && info.Proc.P_stat == zombieState
}
