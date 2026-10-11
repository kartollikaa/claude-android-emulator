package proc

import "syscall"

// Windows' exit code for a process that has not exited.
const stillActive = 259

// Alive reports whether the process with pid runs.
func Alive(pid int) bool {
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var code uint32

	return syscall.GetExitCodeProcess(handle, &code) == nil && code == stillActive
}
