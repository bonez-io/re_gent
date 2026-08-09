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
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
	"github.com/regent-vcs/regent/internal/config"
	"github.com/spf13/cobra"
)

// SetupCmd is the interactive front door: pick projects, wire them, and offer to
// share the wiring. The one-line installer hands over to it, and it can be
// re-run at any time.
func SetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup [server-url]",
		Short: "Pick projects to connect to a re_gent server",
		Long: "Opens a picker of the projects on this machine and wires the ones you\n" +
			"choose. The server URL is remembered after the first run, so later runs\n" +
			"need no argument — `rgt` on its own does the same thing.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			explicit := ""
			if len(args) == 1 {
				explicit = args[0]
			}
			url, err := resolveServerURL(explicit)
			if err != nil {
				return err
			}
			return runSetup(url)
		},
	}
}

// resolveServerURL prefers an explicit argument and otherwise falls back to the
// server remembered from a previous run, so repeat use needs no URL.
func resolveServerURL(explicit string) (string, error) {
	if explicit != "" {
		return strings.TrimRight(explicit, "/"), nil
	}
	cfg, err := config.Load()
	if err == nil && cfg.Server.URL != "" {
		return strings.TrimRight(cfg.Server.URL, "/"), nil
	}
	return "", fmt.Errorf("no server known yet\n\nRun it once with your team server:\n\n  rgt setup <server-url>")
}

// rememberServer stores the server so later runs need no URL. A failure here is
// not worth interrupting a successful setup over — it only costs one argument
// next time.
func rememberServer(url string) {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	if cfg.Server.URL == url {
		return
	}
	cfg.Server.URL = url
	_ = config.Save(cfg)
}

// RunDefaultSetup is what bare `rgt` does: offer to wire more projects into the
// server this machine already knows.
func RunDefaultSetup() error {
	url, err := resolveServerURL("")
	if err != nil {
		return err
	}
	return runSetup(url)
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

// isWired reports whether a project already points at a server.
func isWired(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".regent", "config.toml"))
	return err == nil
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

	picked, err := runPicker(defaultScanRoot(), ttyIn, ttyOut)
	if err != nil {
		return err
	}
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

	rememberServer(serverURL)
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
	wired     bool // already connected; shown so nobody wires it twice
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
			path: m.root, label: filepath.Base(m.root) + "  (this folder)",
			isProject: true, wired: isWired(m.root),
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
			projects = append(projects, pickerEntry{path: full, label: name, isProject: true, wired: isWired(full)})
		} else {
			folders = append(folders, pickerEntry{path: full, label: name + "/"})
		}
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].label < projects[j].label })
	sort.Slice(folders, func(i, j int) bool { return folders[i].label < folders[j].label })
	m.entries = append(m.entries, projects...)
	m.entries = append(m.entries, folders...)
}

// ---------------------------------------------------------------------------
// Arrow-key picker
// ---------------------------------------------------------------------------

var (
	accent  = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true)
	dim     = lipgloss.NewStyle().Foreground(lipgloss.Color("103"))
	heading = lipgloss.NewStyle().Bold(true)
	chosen  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
)

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
	case " ", "x":
		if e := m.current(); e != nil && e.isProject {
			m.selected[e.path] = !m.selected[e.path]
		}
	case "right", "l":
		// Open a folder. On a project this would be ambiguous, so it selects.
		if e := m.current(); e != nil {
			if e.isUp || !e.isProject {
				m.root = e.path
				m.reload()
			} else {
				m.selected[e.path] = true
			}
		}
	case "left", "h":
		if parent := filepath.Dir(m.root); parent != m.root {
			m.root = parent
			m.reload()
		}
	case "a":
		for _, e := range m.entries {
			if e.isProject {
				m.selected[e.path] = true
			}
		}
	case "enter":
		// Enter on a folder opens it; anywhere else it means "go".
		if e := m.current(); e != nil && (e.isUp || !e.isProject) {
			m.root = e.path
			m.reload()
			return m, nil
		}
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

// bannerArt is the re_gent wordmark. Box-drawing glyphs rather than solid
// blocks: they stay legible at any font weight and match the ✔/❯ already used
// below, so the whole screen shares one character vocabulary.
var bannerArt = []string{
	`┬─┐ ┌─┐     ┌─┐ ┌─┐ ┌┐┌ ┌┬┐`,
	`├┬┘ ├┤      │ ┬ ├┤  │││  │ `,
	`┴└─ └─┘ ─── └─┘ └─┘ ┘└┘  ┴ `,
}

// banner renders the opening: a rule, the wordmark, the tagline, another rule.
func banner() string {
	rule := dim.Render(strings.Repeat("·", 62))
	var b strings.Builder
	b.WriteString(rule + "\n\n")
	for _, line := range bannerArt {
		b.WriteString("  " + accent.Render(line) + "\n")
	}
	b.WriteString("\n  " + dim.Render("version control for AI agents") +
		"  " + dim.Render(Version) + "\n")
	b.WriteString(rule + "\n")
	return b.String()
}

func (m pickerModel) View() string {
	var b strings.Builder

	b.WriteString("\n" + banner() + "\n")
	b.WriteString("Let's get started.\n\n")
	b.WriteString(heading.Render("Choose the projects to connect to your team server") + "\n")
	b.WriteString(dim.Render("Space selects, enter connects. Run rgt again any time.") + "\n\n")
	b.WriteString(dim.Render("  "+shortPath(m.root)) + "\n\n")

	if len(m.entries) == 0 {
		b.WriteString(dim.Render("  (nothing here)") + "\n")
	}
	for i, e := range m.entries {
		cursor := "  "
		if i == m.cursor {
			cursor = accent.Render("❯") + " "
		}
		num := fmt.Sprintf("%d.", i+1)

		switch {
		case e.isUp:
			b.WriteString(cursor + dim.Render(num+" ..") + "\n")
		case e.isProject:
			label := num + " " + e.label
			switch {
			case m.selected[e.path]:
				label = chosen.Render(label) + " " + chosen.Render("✔")
			case e.wired:
				label += " " + dim.Render("(already connected)")
			}
			b.WriteString(cursor + label + "\n")
		default:
			b.WriteString(cursor + dim.Render(num+" "+e.label) + "\n")
		}
	}

	b.WriteString("\n" + dim.Render(fmt.Sprintf("  %d selected  ·  ↑↓ move  ·  space select  ·  → open folder  ·  ← back  ·  q quit", len(m.picked()))) + "\n")
	return b.String()
}

// shortPath abbreviates the home prefix, so the header stays readable.
func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// runPicker drives the picker, reading from in and drawing to out. They are
// separate because they are not always the same file: see ttyPair.
func runPicker(root string, in, out *os.File) ([]string, error) {
	p := tea.NewProgram(newPickerModel(root), tea.WithInput(in), tea.WithOutput(out))
	final, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("picker: %w", err)
	}
	m, _ := final.(pickerModel)
	return m.picked(), nil
}
