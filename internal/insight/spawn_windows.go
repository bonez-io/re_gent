//go:build windows

package insight

import "syscall"

// detachAttr is the Windows equivalent of Setsid: a new process group with no
// console attached, so closing the hook's console does not kill the worker.
func detachAttr() *syscall.SysProcAttr {
	const detachedProcess = 0x00000008
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess}
}
