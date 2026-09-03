package insight

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Spawn starts `rgt insight run` for the repository at cwd as a detached
// process: its own session (so a hook's exit or a terminal's SIGHUP never
// reaches it), stdin from /dev/null, stdout and stderr appended to
// .regent/log/insight.log. It returns as soon as the process has started.
//
// exe is the rgt binary to run; callers pass os.Executable() so the worker
// is the same build as the hook that queued the job.
func Spawn(exe, cwd, root string) error {
	if exe == "" {
		return fmt.Errorf("worker executable path is empty")
	}
	logPath := filepath.Join(root, "log", LogFileName)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open worker log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(exe, "insight", "run")
	cmd.Dir = cwd
	cmd.Stdin = nil // /dev/null
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// The worker's output goes to a file, so the style package must not
	// colour it: it honours NO_COLOR, and nothing else reads this variable.
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	// Do not wait; releasing lets the child outlive this process cleanly.
	return cmd.Process.Release()
}
