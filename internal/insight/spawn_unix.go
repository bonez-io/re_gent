//go:build !windows

package insight

import "syscall"

// detachAttr puts the worker in its own session so a hook's exit or a
// terminal's SIGHUP never reaches it.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
