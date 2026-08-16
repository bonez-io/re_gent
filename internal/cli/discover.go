package cli

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// parseSelection turns a picker answer into zero-based indices into n options.
// It accepts "1", "1,3", "2 4" and "a"/"all". An empty answer selects nothing,
// which is how someone backs out. Anything unparseable or out of range is an
// error rather than a silent skip, so a typo never wires a project nobody chose.
func parseSelection(input string, n int) ([]int, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}
	if s := strings.ToLower(input); s == "a" || s == "all" {
		all := make([]int, n)
		for i := range all {
			all[i] = i
		}
		return all, nil
	}

	fields := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	seen := make(map[int]bool, len(fields))
	var out []int
	for _, f := range fields {
		num, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", f)
		}
		if num < 1 || num > n {
			return nil, fmt.Errorf("%d is not between 1 and %d", num, n)
		}
		if idx := num - 1; !seen[idx] {
			seen[idx] = true
			out = append(out, idx)
		}
	}
	return out, nil
}

// interactive reports whether stdin is a terminal we can prompt on. It is false
// under `curl ... | sh`, where stdin is the script itself: prompting there would
// swallow the rest of the script or block forever.
//
// This is a real isatty check, not a character-device test: /dev/null is a
// character device too, so `rgt connect < /dev/null` would otherwise be treated
// as interactive and fail on an EOF it should never have waited for.
func interactive() bool {
	return term.IsTerminal(os.Stdin.Fd())
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
// canPrompt is passed in rather than read from os.Stdin here so the picker is
// testable without a pty.
func connectPicked(serverURL, root string, out io.Writer, in io.Reader, canPrompt bool) error {
	projects := discoverProjects(root, maxDiscoveryDepth)
	if len(projects) == 0 {
		return fmt.Errorf("no projects found in %s\n\nRun this inside a project, or from a directory that contains your projects", root)
	}

	// Without a terminal we cannot ask, so say what we found and what to run
	// rather than picking something nobody chose.
	if !canPrompt {
		fmt.Fprintf(out, "Found %d project(s) under %s. Run connect inside the one you want:\n\n", len(projects), root)
		for _, p := range projects {
			fmt.Fprintf(out, "  cd %s && rgt connect %s\n", p, serverURL)
		}
		return nil
	}

	fmt.Fprintf(out, "Projects under %s:\n\n", root)
	for i, p := range projects {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			rel = p
		}
		fmt.Fprintf(out, "  %d) %s\n", i+1, rel)
	}
	fmt.Fprintf(out, "\nWire which? (e.g. 1, or 1,3, or 'a' for all; Enter to cancel): ")

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("read selection: %w", err)
	}
	picks, err := parseSelection(line, len(projects))
	if err != nil {
		return fmt.Errorf("invalid selection: %w", err)
	}
	if len(picks) == 0 {
		fmt.Fprintln(out, "Nothing selected.")
		return nil
	}

	// One project's failure must not strand the rest: report and continue, then
	// fail overall so a scripted caller still sees something went wrong.
	var failed int
	for _, i := range picks {
		fmt.Fprintf(out, "\n== %s ==\n", filepath.Base(projects[i]))
		if err := runConnect(connectParams{serverURL: serverURL, projectRoot: projects[i]}); err != nil {
			failed++
			fmt.Fprintf(out, "  ! %v\n", err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d project(s) failed to connect", failed, len(picks))
	}
	return nil
}
