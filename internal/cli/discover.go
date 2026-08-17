package cli

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/x/term"
)

// maxDiscoveryDepth bounds how far below the starting directory we look for
// projects. Two levels covers the layouts people actually use
// (~/code/<project> and ~/code/<org>/<project>) without walking an entire home
// directory.
const maxDiscoveryDepth = 2

// skipDirs are never descended into: they hold thousands of files and never
// contain a project someone means to wire.
var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
	"Library":      true,
}

// discoverProjects returns directories at or below root that look like projects
// — they contain a .git entry — sorted for a stable menu. A discovered project
// is not descended into, so submodules and nested checkouts do not clutter the
// list with entries belonging to a parent already shown.
func discoverProjects(root string, maxDepth int) []string {
	var found []string
	rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		// Unreadable entries are skipped rather than fatal: a permission error
		// deep in a home directory must not abandon the whole scan.
		if err != nil || !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != root && (skipDirs[name] || strings.HasPrefix(name, ".")) {
			return fs.SkipDir
		}
		if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil {
			found = append(found, path)
			return fs.SkipDir
		}
		if strings.Count(filepath.Clean(path), string(os.PathSeparator))-rootDepth >= maxDepth {
			return fs.SkipDir
		}
		return nil
	})

	sort.Strings(found)
	return found
}

// projectChooser presents the projects under root and returns the ones the user
// chose. It exists so that connect and setup share one picker rather than each
// carrying its own.
//
// It is a function type rather than a direct call to runPicker because the
// picker needs a real terminal and a test does not have one. Injecting the
// choice keeps "which projects were chosen" separate from "what happens to
// them", and the second half is the part worth testing.
type projectChooser func(root string) ([]string, error)

// chooseWithPicker is the production chooser: the full-screen picker, the same
// one `rgt setup --interactive` presents.
//
// The alternative it replaces was a numbered list read a line at a time, with
// its own selection parser accepting "1,3", "2 4" and "a" for all. Two pickers
// meant two answers to the same questions — whether backing out is an error,
// whether you can see which projects are already connected, how far below the
// current directory to look. This one navigates, shows wired state, and does
// not let a single keystroke wire every repository it happened to find.
func chooseWithPicker(root string) ([]string, error) {
	in, out, cleanup, err := ttyPair()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return runPicker(root, in, out)
}

// isTerminal reports whether f is a terminal.
//
// This is the one terminal check in the package. There were two: a named helper
// that only ever looked at stdin, and a bare term.IsTerminal call on stdout
// inside ttyPair. Two probes meant the reasoning below was written down in one
// place and silently assumed in the other, and neither name said which handle it
// meant — so "is this interactive?" had two answers depending on which you
// happened to call.
//
// The two handles genuinely differ and both matter. Under `curl ... | sh` stdin
// is the script itself, so prompting swallows the rest of the script or blocks
// forever. The installer meanwhile runs `rgt setup <url> < /dev/tty`, and a
// shell `<` redirect opens the terminal read-only, so reusing that stdin for
// output makes every draw fail and the picker look frozen. Taking the handle as
// an argument makes each caller say which one it is asking about.
//
// It is a real isatty check, not a character-device test: /dev/null is a
// character device too, so `rgt connect < /dev/null` would otherwise be treated
// as interactive and fail on an EOF it should never have waited for.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(f.Fd())
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

// connectPicked lists the projects below root and connects the chosen ones. It
// runs when connect is invoked outside a project, where the alternative is
// cd-ing into each one and repeating the command.
//
// canPrompt and choose are both passed in rather than read from os.Stdin here,
// so this path is testable without a pty — which is what let the empty-selection
// disagreement with setup go unnoticed.
// shareFunc is asked, after projects have been wired, whether to commit the
// wiring so teammates get it on clone. A nil shareFunc asks nothing, which is
// what a run with no terminal — and every test — wants.
type shareFunc func(projects []string)

// applyConnections wires each chosen project, or unwires one that is already
// wired, and reports what happened per project.
//
// In the picker, selecting an already-connected project means "disconnect it":
// the tick expresses the state you want, not one fixed verb.
//
// That reading only holds for a tick. `rgt connect` typed inside a project is
// an imperative, and answering it by disconnecting is how connecting a project
// twice used to remove its hooks. allowDisconnect is which of the two the
// caller is: a state expressed, or an instruction given.
//
// One project's failure must not strand the rest, so failures are counted and
// the loop continues.
// repoID, when non-empty, is the identity to register under instead of a
// derived one. It only ever arrives from the single-project route: naming one
// identity for a batch of projects picked from a list would give them all the
// same one, which is the collision this identity work exists to remove.
func applyConnections(serverURL string, picked []string, out io.Writer, allowDisconnect bool, repoID string) (wired []string, unwired, failed int) {
	for _, p := range picked {
		fmt.Fprintf(out, "\n== %s ==\n", filepath.Base(p))
		if allowDisconnect && isConnected(p) {
			if err := reportDisconnect(p); err != nil {
				failed++
				fmt.Fprintf(out, "  ! %v\n", err)
				continue
			}
			unwired++
			continue
		}
		if err := runConnect(connectParams{serverURL: serverURL, projectRoot: p, repoID: repoID}); err != nil {
			failed++
			fmt.Fprintf(out, "  ! %v\n", err)
			continue
		}
		wired = append(wired, p)
	}
	return wired, unwired, failed
}

func connectPicked(serverURL, root string, out io.Writer, canPrompt bool, choose projectChooser, share shareFunc) error {
	projects := discoverProjects(root, maxDiscoveryDepth)
	if len(projects) == 0 {
		return fmt.Errorf("no projects found in %s\n\nRun this inside a project, or from a directory that contains your projects", root)
	}

	// Without a terminal we cannot ask, so say what we found and what to run
	// rather than picking something nobody chose.
	//
	// The suggestions are guidance, not an outcome. Returning nil here made
	// `rgt connect <url>` exit 0 from a directory of repositories having
	// connected none of them — the same shape as the init summary bug, where a
	// command that did nothing reported as though it had. A person reads the
	// list and acts on it; an installer reads the exit code and moves on, and
	// capture silently never happens. So: print the guidance, then fail.
	if !canPrompt {
		fmt.Fprintf(out, "Found %d project(s) under %s. Run connect inside the one you want:\n\n", len(projects), root)
		for _, p := range projects {
			fmt.Fprintf(out, "  cd %s && rgt connect %s\n", p, serverURL)
		}
		return fmt.Errorf("nothing connected: %s is not a project, and there is no terminal to choose one on", root)
	}

	picked, err := choose(root)
	if err != nil {
		return err
	}

	// Backing out is a legitimate choice and not an error in itself, but it did
	// not connect anything, and the caller has to be able to tell. setup has
	// always failed here; connect printed "Nothing selected." and exited 0, so
	// the same decision produced opposite answers depending on which command you
	// reached it through. One picker, one answer.
	if len(picked) == 0 {
		return fmt.Errorf("nothing selected — no projects were connected")
	}

	wired, unwired, failed := applyConnections(serverURL, picked, out, true, "")
	if failed > 0 {
		return fmt.Errorf("%d of %d project(s) failed to connect", failed, len(picked))
	}

	// Only offer to share what was actually wired. Asking about a project that
	// was just disconnected would propose committing config that is now gone.
	if len(wired) > 0 {
		rememberServer(serverURL)
		if share != nil {
			share(wired)
		}
	}
	if unwired > 0 {
		fmt.Fprintf(out, "\n%d project(s) disconnected.\n", unwired)
	}
	return nil
}

// shareWithTeam is the production shareFunc. It opens its own terminal handle
// because the picker has already closed the one it used, and a failure to find
// one simply means the question goes unasked.
func shareWithTeam(projects []string) {
	in, _, cleanup, err := ttyPair()
	if err != nil {
		return
	}
	defer cleanup()
	offerShare(projects, in)
}
