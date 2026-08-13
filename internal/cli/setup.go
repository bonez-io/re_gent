package cli

import (
	"fmt"
	"io"
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
	var interactiveSetup bool

	cmd := &cobra.Command{
		Use:   "setup [server-url]",
		Short: "Connect this project to a re_gent server",
		Long: "Wires the project in the current directory to the server. The URL is\n" +
			"remembered after the first run, so later runs need no argument — `rgt`\n" +
			"on its own does the same thing. Pass --interactive to pick from a list\n" +
			"of projects on this machine instead.",
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
			return runSetup(url, setupOptions{interactive: interactiveSetup})
		},
	}

	cmd.Flags().BoolVar(&interactiveSetup, "interactive", false, "Choose projects from a picker instead of wiring the current directory")

	return cmd
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
//
// This one is interactive by default, unlike `rgt setup <url>`. Bare `rgt` is
// typed by a person at a terminal who is exploring, and the picker is the point
// of it. The installer never takes this path — it always passes a URL — so the
// pasted command still runs without prompting.
func RunDefaultSetup() error {
	url, err := resolveServerURL("")
	if err != nil {
		return err
	}
	return runSetup(url, setupOptions{interactive: true})
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

// setupOptions selects how setup chooses projects. The zero value is the
// default: wire the project we are standing in, ask nothing.
type setupOptions struct {
	interactive bool // --interactive: scan and present the picker
}

// setupTargets decides which project directories a setup run should wire.
//
// The default wires only the current directory. The installer runs from inside
// the repo the teammate cares about, so that is the one they mean; scanning and
// wiring everything found under a scan root would let a single pasted command
// reach into unrelated repositories nobody chose.
//
// Outside a project this returns an error rather than an empty list. The
// previous behaviour — print a suggestion and return nil — is what let the
// installer exit 0 having wired nothing, with no failure for the script to see.
func setupTargets(cwd string, opts setupOptions) ([]string, error) {
	if opts.interactive {
		return nil, nil // caller runs the picker
	}

	if !isProjectDir(cwd) {
		return nil, fmt.Errorf("%s is not a project (no .git or .regent)\n\nRun this from inside the repository you want to capture, or pass --interactive to choose from a list", cwd)
	}
	return []string{cwd}, nil
}

func runSetup(serverURL string, opts setupOptions) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	picked, err := setupTargets(cwd, opts)
	if err != nil {
		return err
	}

	var ttyIn *os.File
	if picked == nil {
		ttyIn2, ttyOut, cleanup, ttyErr := ttyPair()
		if ttyErr != nil {
			return fmt.Errorf("--interactive needs a terminal, and none is available here: %w", ttyErr)
		}
		defer cleanup()
		ttyIn = ttyIn2

		picked, err = runPicker(defaultScanRoot(), ttyIn, ttyOut)
		if err != nil {
			return err
		}
		if len(picked) == 0 {
			return fmt.Errorf("nothing selected — no projects were connected")
		}
	}

	// A selected project that is already connected means "disconnect it": the
	// tick expresses the change you want, not a single fixed verb.
	var wired []string
	var unwired int
	for _, p := range picked {
		fmt.Printf("\n== %s ==\n", filepath.Base(p))
		if isWired(p) {
			if err := reportDisconnect(p); err != nil {
				fmt.Printf("  ! %v\n", err)
				continue
			}
			unwired++
			continue
		}
		if err := runConnect(connectParams{serverURL: serverURL, projectRoot: p}); err != nil {
			fmt.Printf("  ! %v\n", err)
			continue
		}
		wired = append(wired, p)
	}
	if len(wired) == 0 && unwired == 0 {
		return fmt.Errorf("nothing was changed")
	}
	if len(wired) == 0 {
		fmt.Printf("\nDone. %d project(s) disconnected.\n", unwired)
		return nil
	}

	rememberServer(serverURL)
	// Sharing is a question, so it only happens on the path that has a terminal
	// to ask on. The default path wires and reports; it never blocks a paste.
	if ttyIn != nil {
		offerShare(wired, ttyIn)
	}

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
func offerShare(projects []string, in io.Reader) {
	for _, p := range projects {
		if !isDir(filepath.Join(p, ".git")) {
			continue // not a git repo; nothing to share through
		}
		fmt.Printf("\nShare %s with your team? Commits %s (no push) [y/N]: ",
			filepath.Base(p), strings.Join(sharedFiles, " and "))
		answer, err := readAnswer(in)
		if err != nil && answer == "" {
			fmt.Println("  skipped")
			return // input closed; asking again would spin
		}
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

// readAnswer reads one typed line, ending at EITHER newline or carriage return.
// That distinction is the whole point: a full-screen UI can hand the terminal
// back with ICRNL cleared, so Enter arrives as \r. A reader waiting only for \n
// then blocks forever while the keystroke echoes — indistinguishable, from the
// outside, from a prompt that ignores input.
func readAnswer(r io.Reader) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			switch c := buf[0]; c {
			case '\n', '\r':
				return string(line), nil
			case 3, 4: // ctrl-c / ctrl-d: treat as declining
				return "", io.EOF
			default:
				line = append(line, c)
			}
		}
		if err != nil {
			return string(line), err
		}
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
	isNone    bool // the explicit "connect nothing" row
	wired     bool // already connected; shown so nobody wires it twice
}

type pickerModel struct {
	root     string
	entries  []pickerEntry
	cursor   int
	selected map[string]bool // keyed by absolute path, so it survives navigation
	done     bool
	aborted  bool
	hint     string // shown when a key could not do what was asked
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
	// An explicit way out. Leaving without connecting should be a visible
	// choice, not something you have to know a key for.
	m.entries = append(m.entries, pickerEntry{label: "Nothing — don't connect anything", isNone: true})
}

// ---------------------------------------------------------------------------
// Arrow-key picker
// ---------------------------------------------------------------------------

var (
	accent  = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true)
	dim     = lipgloss.NewStyle().Foreground(lipgloss.Color("103"))
	heading = lipgloss.NewStyle().Bold(true)
	chosen  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warn    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
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
		m.hint = ""
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		m.hint = ""
		if m.cursor < len(m.entries)-1 {
			m.cursor++
		}
	case " ", "x":
		m.hint = ""
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
		e := m.current()
		switch {
		case e == nil:
			return m, nil
		case e.isNone:
			// Chose to connect nothing: leave without wiring anything.
			m.aborted = true
			return m, tea.Quit
		case e.isUp || !e.isProject:
			m.hint = ""
			m.root = e.path
			m.reload()
			return m, nil
		case len(m.picked()) == 0:
			// Refuse to "confirm" an empty selection: silently connecting
			// nothing looks identical to a broken key.
			m.hint = "Nothing marked yet — press space to mark a project, or choose \"Nothing\" to leave."
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
	b.WriteString(heading.Render("Connect or disconnect projects") + "\n")
	b.WriteString(dim.Render("Space marks a project. Connected ones get disconnected. Run rgt any time.") + "\n\n")
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
		case e.isNone:
			b.WriteString(cursor + dim.Render(num+" "+e.label) + "\n")
		case e.isProject:
			label := num + " " + e.label
			switch {
			case m.selected[e.path] && e.wired:
				label = warn.Render(label) + " " + warn.Render("✗ disconnect")
			case m.selected[e.path]:
				label = chosen.Render(label) + " " + chosen.Render("✔ connect")
			case e.wired:
				label += " " + dim.Render("(connected)")
			}
			b.WriteString(cursor + label + "\n")
		default:
			b.WriteString(cursor + dim.Render(num+" "+e.label) + "\n")
		}
	}

	if m.hint != "" {
		b.WriteString("\n  " + warn.Render(m.hint) + "\n")
	}
	b.WriteString("\n" + dim.Render(fmt.Sprintf("  %d marked  ·  ↑↓ move  ·  space select  ·  → open folder  ·  ← back  ·  q quit", len(m.picked()))) + "\n")
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
