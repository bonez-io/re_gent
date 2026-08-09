package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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

// openTTY returns a handle on the controlling terminal. stdin is not it under
// `curl ... | sh` — there stdin is the script being read — so we open /dev/tty
// directly, which still refers to the real terminal. The error case is a
// genuine absence of any terminal (CI, cron), where the caller prints
// instructions instead of waiting for input nobody can give.
func openTTY() (*os.File, error) {
	if interactive() {
		return os.Stdin, nil
	}
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
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
	tty, err := openTTY()
	if err != nil {
		fmt.Printf("No terminal available, so nothing was wired.\n\nRun this inside a project:\n\n  rgt connect %s\n\n", serverURL)
		return nil
	}
	if tty != os.Stdin {
		defer tty.Close()
	}

	m, err := runPicker(defaultScanRoot(), tty)
	if err != nil {
		return err
	}
	picked := m.picked()
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

	offerShare(wired, bufio.NewReader(tty))

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

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c", "q", "esc":
		m.aborted = true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.entries)-1 {
			m.cursor++
		}
	case " ":
		if e := m.current(); e != nil && e.isProject {
			m.selected[e.path] = !m.selected[e.path]
		}
	case "enter", "right", "l":
		// Enter opens a folder; on a project it means "this one, go".
		if e := m.current(); e != nil {
			if e.isUp || !e.isProject {
				m.root = e.path
				m.reload()
				return m, nil
			}
			m.selected[e.path] = true
			m.done = true
			return m, tea.Quit
		}
	case "left", "h":
		if parent := filepath.Dir(m.root); parent != m.root {
			m.root = parent
			m.reload()
		}
	case "c":
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m pickerModel) current() *pickerEntry {
	if m.cursor < 0 || m.cursor >= len(m.entries) {
		return nil
	}
	return &m.entries[m.cursor]
}

func (m pickerModel) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Connect projects to re_gent\n  %s\n\n", m.root)

	if len(m.entries) == 0 {
		b.WriteString("  (empty)\n")
	}
	for i, e := range m.entries {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		switch {
		case e.isUp, !e.isProject:
			fmt.Fprintf(&b, "%s    %s\n", cursor, e.label)
		default:
			mark := " "
			if m.selected[e.path] {
				mark = "x"
			}
			fmt.Fprintf(&b, "%s[%s] %s\n", cursor, mark, e.label)
		}
	}

	fmt.Fprintf(&b, "\n  %d selected\n", len(m.picked()))
	b.WriteString("  ↑↓ move · space select · enter open folder · c connect · q quit\n")
	return b.String()
}

// runPicker drives the TUI on the given terminal.
func runPicker(root string, tty *os.File) (pickerModel, error) {
	p := tea.NewProgram(newPickerModel(root), tea.WithInput(tty), tea.WithOutput(tty))
	out, err := p.Run()
	if err != nil {
		return pickerModel{}, fmt.Errorf("picker: %w", err)
	}
	m, _ := out.(pickerModel)
	return m, nil
}
