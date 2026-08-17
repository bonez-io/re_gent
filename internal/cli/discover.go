package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/x/term"
)

// This file holds what connect needs to decide where it is and what to do
// there. It used to hold a filesystem crawler and a full-screen picker over its
// results as well; both are gone (#28) — see notAProject for why.

// isTerminal reports whether f is a terminal.
//
// This is the one terminal check in the package. There were two: a named helper
// that only ever looked at stdin, and a bare term.IsTerminal call inside the
// picker's tty handling. Two probes meant the reasoning below was written down
// in one place and silently assumed in the other.
//
// The handle that matters is stdin, and it is asked about for one reason: under
// `curl <server>/install.sh | sh` stdin is the script itself, so prompting
// swallows the rest of the script or blocks forever. Taking the handle as an
// argument makes the caller say which one it is asking about.
//
// It is a real isatty check, not a character-device test: /dev/null is a
// character device too, so `rgt connect < /dev/null` would otherwise be treated
// as interactive and fail on an EOF it should never have waited for.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(f.Fd())
}

// notAProject is connect's answer when it is run somewhere that is not a
// project. It names the fix and touches nothing.
//
// The previous answer was to search: walk the directories below, list every
// repository found, and — with a terminal — open a picker over them. That put
// projects nobody named within one keypress of being wired, or, if they were
// wired already, disconnected (#28). A command run in the wrong directory has
// one useful reply, and it is where to run it instead.
func notAProject(dir, serverURL string) error {
	return fmt.Errorf("%s is not a project: no .git or .regent here.\n\n"+
		"Go to the project you want to connect and run it there:\n\n"+
		"  cd <your-project>\n  rgt connect %s", dir, serverURL)
}

// isProjectDir reports whether dir is somewhere connect should act directly: a
// git checkout, or a directory already wired to re_gent.
func isProjectDir(dir string) bool {
	for _, marker := range []string{".git", ".regent"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// shareWithTeam asks, after a project has been wired, whether to commit the
// wiring so teammates get it on clone.
//
// It reads stdin directly. It used to open its own /dev/tty pair, because the
// picker had taken the terminal over and handed back a handle that could not be
// written to; with the picker gone there is no second handle to reconcile, and
// the only caller reaches this after checking isTerminal(os.Stdin) — so stdin
// is a person, not a script being swallowed.
func shareWithTeam(projects []string) {
	offerShare(projects, os.Stdin)
}
