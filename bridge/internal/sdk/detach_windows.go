package sdk

import "syscall"

// DETACHED_PROCESS, which the syscall package does not name.
const detachedProcess = 0x00000008

func detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess}
}
