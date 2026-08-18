package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
)

var verboseOutput atomic.Bool

// SetVerbose controls diagnostic output for commands in this process.
func SetVerbose(enabled bool) { verboseOutput.Store(enabled) }

// Verbose reports whether diagnostics were explicitly requested. The
// environment form keeps direct subcommand tests and shell integrations useful
// even when they do not execute the root command's persistent flag handling.
func Verbose() bool {
	if verboseOutput.Load() {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("REGENT_VERBOSE"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func Verbosef(w io.Writer, format string, args ...interface{}) {
	if !Verbose() {
		return
	}
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, format, args...)
}

// runSetupCommand keeps package-manager and provisioning chatter out of the
// normal onboarding surface. Verbose mode reconnects the child directly to the
// terminal so its native diagnostics remain available when troubleshooting.
func runSetupCommand(cmd *exec.Cmd, label string) error {
	if Verbose() {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		return nil
	}
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s failed; re-run with --verbose for diagnostics", label)
	}
	return nil
}
