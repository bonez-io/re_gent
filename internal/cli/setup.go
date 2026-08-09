package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

// SetupCmd is the interactive front door: pick projects, wire them, and offer to
// share the wiring. The one-line installer hands over to it, and it can be
// re-run at any time.
func SetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "setup <server-url>",
		Short:        "Pick projects to connect to a re_gent server",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup(strings.TrimRight(args[0], "/"))
		},
	}
}

// ttyPair returns the handles the interactive UI reads from and draws to, plus
// a cleanup func. The error case is a genuine absence of any terminal (CI,
// cron), where the caller prints instructions rather than waiting for input
// nobody can give.
//
// Opening /dev/tty O_RDWR is deliberate and comes first. The installer invokes
// `rgt setup <url> < /dev/tty`, and a shell `<` redirect opens the terminal
// READ-ONLY: reusing that stdin as the output handle makes every draw fail, so
// the picker never paints and looks frozen. stdin/stdout are only a fallback,
// and even then output goes to stdout, never back at the input handle.
func ttyPair() (in *os.File, out *os.File, cleanup func(), err error) {
	var toClose []*os.File
	cleanup = func() {
		for _, f := range toClose {
			_ = f.Close()
		}
	}

	// Read from stdin whenever it is already a terminal. Read-only is fine for
	// reading, and it is the handle the TUI library drives most reliably — a
	// separately-opened /dev/tty renders but never delivers keystrokes.
	if interactive() {
		in = os.Stdin
	} else if f, e := os.OpenFile("/dev/tty", os.O_RDONLY, 0); e == nil {
		in, toClose = f, append(toClose, f)
	}

	// Draw to stdout when it is a terminal, else to /dev/tty. Never back at the
	// input handle: the installer runs this with `< /dev/tty`, whose read-only
	// fd silently drops every write and leaves the picker looking frozen.
	if term.IsTerminal(os.Stdout.Fd()) {
		out = os.Stdout
	} else if f, e := os.OpenFile("/dev/tty", os.O_WRONLY, 0); e == nil {
		out, toClose = f, append(toClose, f)
	}

	if in == nil || out == nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("no controlling terminal")
	}
	return in, out, cleanup, nil
}

// defaultScanRoot picks where browsing starts: the current directory when it
// holds projects (someone who cd'd to their code folder meant that folder),
// else a conventional code directory, else home.
func defaultScanRoot() string {
	if cwd, err := os.Getwd(); err == nil {
		if isProjectDir(cwd) || len(discoverProjects(cwd, 1)) > 0 {
			return cwd
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	for _, c := range []string{"Documents/GitHub", "Documents/code", "code", "src", "Projects"} {
		if p := filepath.Join(home, c); isDir(p) {
			return p
		}
	}
	return home
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func runSetup(serverURL string) error {
	ttyIn, ttyOut, cleanup, err := ttyPair()
	if err != nil {
		fmt.Printf("No terminal available, so nothing was wired.\n\nRun this inside a project:\n\n  rgt connect %s\n\n", serverURL)
		return nil
	}
	defer cleanup()

	picked := runTextPicker(defaultScanRoot(), bufio.NewReader(ttyIn), ttyOut)
	if len(picked) == 0 {
		fmt.Println("Nothing selected — no projects were connected.")
		return nil
	}

	// Wire first, share second: sharing only makes sense for what actually got
	// wired, and connect prints its own per-project progress.
	var wired []string
	for _, p := range picked {
		fmt.Printf("\n== %s ==\n", filepath.Base(p))
		if err := runConnect(connectParams{serverURL: serverURL, projectRoot: p}); err != nil {
			fmt.Printf("  ! %v\n", err)
			continue
		}
		wired = append(wired, p)
	}
	if len(wired) == 0 {
		return fmt.Errorf("no projects were connected")
	}

	offerShare(wired, bufio.NewReader(ttyIn))

	fmt.Printf("\nDone. Open the viewer to watch turns arrive.\n")
	fmt.Printf("Restart any Claude Code / Codex session already open in these projects —\n")
	fmt.Printf("agents load hooks at startup.\n")
	return nil
}

// sharedFiles are the only paths the share step ever commits: the server wiring
// and the hook config. The rest of .regent/ stays git-ignored.
var sharedFiles = []string{".regent/config.toml", ".claude/settings.json"}

// offerShare asks, per project, whether to commit the wiring so teammates get it
// on clone. It never pushes: reaching a shared remote is the user's call, not a
// side effect of a setup wizard.
func offerShare(projects []string, in *bufio.Reader) {
	for _, p := range projects {
		if !isDir(filepath.Join(p, ".git")) {
			continue // not a git repo; nothing to share through
		}
		fmt.Printf("\nShare %s with your team? Commits %s (no push) [y/N]: ",
			filepath.Base(p), strings.Join(sharedFiles, " and "))
		answer, _ := in.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Println("  skipped")
			continue
		}
		if err := commitWiring(p); err != nil {
			// A failure here (no git identity, a pre-commit hook, an ignored
			// path) must not undo the wiring, which already succeeded.
			fmt.Printf("  ! could not commit: %v\n", err)
			fmt.Printf("    the project is still connected; commit these yourself when ready\n")
			continue
		}
		fmt.Printf("  ✓ committed — teammates who pull get the wiring automatically\n")
	}
}

// commitWiring stages the two wiring files and commits ONLY those paths. The
// pathspec form matters: a plain `git commit` would sweep in whatever else the
// user had staged, under a message about re_gent.
func commitWiring(dir string) error {
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	var present []string
	for _, f := range sharedFiles {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			present = append(present, f)
		}
	}
	if len(present) == 0 {
		return fmt.Errorf("nothing to commit")
	}
	if out, err := git(append([]string{"add", "--"}, present...)...); err != nil {
		return fmt.Errorf("git add: %s", firstLine(out))
	}
	args := append([]string{"commit", "-m", "Wire re_gent to the team server", "--"}, present...)
	if out, err := git(args...); err != nil {
		if strings.Contains(out, "nothing to commit") || strings.Contains(out, "no changes added") {
			return fmt.Errorf("already committed")
		}
		return fmt.Errorf("%s", firstLine(out))
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "unknown error"
	}
	return s
}

// ---------------------------------------------------------------------------
// The picker
// ---------------------------------------------------------------------------

type pickerEntry struct {
	path      string
	label     string
	isProject bool
	isUp      bool
}

type pickerModel struct {
	root     string
	entries  []pickerEntry
	cursor   int
	selected map[string]bool // keyed by absolute path, so it survives navigation
	done     bool
	aborted  bool
}

// picked returns the chosen project paths in a stable order.
func (m pickerModel) picked() []string {
	if m.aborted {
		return nil
	}
	var out []string
	for p, ok := range m.selected {
		if ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func newPickerModel(root string) pickerModel {
	m := pickerModel{root: root, selected: map[string]bool{}}
	m.reload()
	return m
}

// reload lists the current directory: an "up" row, any projects (selectable),
// then plain folders (navigable). Projects come first so the common case sits
// under the cursor immediately.
func (m *pickerModel) reload() {
	m.cursor = 0
	m.entries = nil

	if parent := filepath.Dir(m.root); parent != m.root {
		m.entries = append(m.entries, pickerEntry{path: parent, label: "..", isUp: true})
	}
	if isProjectDir(m.root) {
		m.entries = append(m.entries, pickerEntry{
			path: m.root, label: filepath.Base(m.root) + "  (this folder)", isProject: true,
		})
	}

	items, err := os.ReadDir(m.root)
	if err != nil {
		return
	}
	var projects, folders []pickerEntry
	for _, it := range items {
		name := it.Name()
		if !it.IsDir() || strings.HasPrefix(name, ".") || skipDirs[name] {
			continue
		}
		full := filepath.Join(m.root, name)
		if isProjectDir(full) {
			projects = append(projects, pickerEntry{path: full, label: name, isProject: true})
		} else {
			folders = append(folders, pickerEntry{path: full, label: name + "/"})
		}
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].label < projects[j].label })
	sort.Slice(folders, func(i, j int) bool { return folders[i].label < folders[j].label })
	m.entries = append(m.entries, projects...)
	m.entries = append(m.entries, folders...)
}

// runTextPicker lists projects and reads plain lines, deliberately not a
// full-screen TUI. The keystroke-driven version rendered correctly but never
// received input across every terminal configuration tried; a line-based prompt
// reads from the same handle reliably, and works over SSH and in dumb terminals
// where a raw-mode UI does not.
func runTextPicker(root string, in *bufio.Reader, out io.Writer) []string {
	m := newPickerModel(root)

	for {
		fmt.Fprintf(out, "\n  Connect projects to re_gent\n  %s\n\n", m.root)
		if len(m.entries) == 0 {
			fmt.Fprintf(out, "  (nothing here)\n")
		}
		for i, e := range m.entries {
			switch {
			case e.isUp:
				fmt.Fprintf(out, "  %2d) ..            (up one level)\n", i+1)
			case e.isProject:
				mark := " "
				if m.selected[e.path] {
					mark = "x"
				}
				fmt.Fprintf(out, "  %2d) [%s] %s\n", i+1, mark, e.label)
			default:
				fmt.Fprintf(out, "  %2d)     %s\n", i+1, e.label)
			}
		}
		fmt.Fprintf(out, "\n  %d selected\n", len(m.picked()))
		fmt.Fprintf(out, "  number = open folder / tick project · a = all here · c = connect · q = quit\n> ")

		line, err := in.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return nil // input closed; treat as cancel rather than looping forever
		}
		switch cmd := strings.ToLower(strings.TrimSpace(line)); cmd {
		case "q", "quit":
			return nil
		case "c", "":
			return m.picked()
		case "a", "all":
			for _, e := range m.entries {
				if e.isProject {
					m.selected[e.path] = true
				}
			}
			continue
		default:
			picks, perr := parseSelection(cmd, len(m.entries))
			if perr != nil || len(picks) == 0 {
				fmt.Fprintf(out, "  ! %v\n", perr)
				continue
			}
			// A single entry that is a folder means "go there"; anything else
			// toggles the projects named.
			if len(picks) == 1 {
				if e := m.entries[picks[0]]; e.isUp || !e.isProject {
					m.root = e.path
					m.reload()
					continue
				}
			}
			for _, i := range picks {
				if e := m.entries[i]; e.isProject {
					m.selected[e.path] = !m.selected[e.path]
				}
			}
		}
	}
}
