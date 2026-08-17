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
	"github.com/pelletier/go-toml/v2"
	"github.com/regent-vcs/regent/internal/config"
	"github.com/regent-vcs/regent/internal/store"
)

// SetupCmd is the interactive front door: pick projects, wire them, and offer to
// share the wiring. The one-line installer hands over to it, and it can be
// re-run at any time.
func resolveServerURL(explicit string) (string, error) {
	if explicit != "" {
		return strings.TrimRight(explicit, "/"), nil
	}
	cfg, err := config.Load()
	if err == nil && cfg.Server.URL != "" {
		return strings.TrimRight(cfg.Server.URL, "/"), nil
	}
	return "", fmt.Errorf("no server known yet\n\nRun it once with your team server:\n\n  rgt connect <server-url>")
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
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	return connectPicked(url, cwd, os.Stdout, isTerminal(os.Stdin), chooseWithPicker, shareWithTeam)
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
	if isTerminal(os.Stdin) {
		in = os.Stdin
	} else if f, e := os.OpenFile("/dev/tty", os.O_RDONLY, 0); e == nil {
		in, toClose = f, append(toClose, f)
	}

	// Draw to stdout when it is a terminal, else to /dev/tty. Never back at the
	// input handle: the installer runs this with `< /dev/tty`, whose read-only
	// fd silently drops every write and leaves the picker looking frozen.
	if isTerminal(os.Stdout) {
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
// isConnected is the single answer to "is this project bound to a server".
//
// It requires BOTH a server address and a project identity, because either one
// alone is not a binding. The previous check was the existence of
// .regent/config.toml — but `rgt init` writes that file unconditionally, so
// every project anyone had used locally already looked connected. Connecting
// one then took the disconnect branch: it reported "not connected to a server",
// changed nothing, and exited non-zero; and where it got further, it removed
// the agent hooks and called that success.
//
// Two facts disagreeing about one word is what caused that. There is one fact
// now.
func isConnected(dir string) bool {
	cfg, err := readRemoteConfig(dir)
	if err != nil {
		return false
	}
	return cfg.URL != "" && cfg.RepoID != ""
}

// readRemoteConfig reads a project's server binding without opening a store.
// A missing or unparseable file is simply "no binding", which is what every
// caller means by it.
func readRemoteConfig(dir string) (store.RemoteConfig, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".regent", "config.toml"))
	if err != nil {
		return store.RemoteConfig{}, err
	}
	var cfg store.RepoConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return store.RemoteConfig{}, err
	}
	return cfg.Remote, nil
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
		// .gitignore is named because sharing the binding requires changing it:
		// it lives inside the directory projects exclude. Committing a file the
		// user was not told about is how a helpful step becomes a surprise.
		fmt.Printf("\nShare %s with your team? Commits %s (plus .gitignore, if it excludes the binding; no push) [y/N]: ",
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

	// The binding lives inside the directory the project excludes, so sharing it
	// means changing that exclusion. Doing it here rather than at init time is
	// deliberate: this is the moment the user said yes to sharing, and rewriting
	// somebody's .gitignore is not something a setup step should do uninvited.
	narrowed, err := narrowRegentExclusion(dir)
	if err != nil {
		return fmt.Errorf("narrow .regent/ exclusion: %w", err)
	}
	if narrowed {
		present = append(present, ".gitignore")
	}

	// -f because narrowing the root .gitignore is not the end of the ways a
	// project can exclude this path: a global core.excludesFile, .git/info/exclude,
	// or a pattern this cannot safely rewrite. The user asked for the binding to
	// be shared; refusing over an ignore rule would leave the clone knowing
	// nothing, which is the failure being fixed (#12).
	if out, err := git(append([]string{"add", "-f", "--"}, present...)...); err != nil {
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

// regentExclusionLines are the spellings of "exclude the whole .regent
// directory" that a root .gitignore is written with.
var regentExclusionLines = map[string]bool{
	".regent": true, ".regent/": true, "/.regent": true, "/.regent/": true,
}

// narrowedRegentExclusion is what replaces them: the directory stays visible so
// git descends into it, its contents are excluded, and the binding is
// re-included by name.
const narrowedRegentExclusion = `# re_gent local state — machine-specific, like .git/ itself.
# config.toml is the exception: it is the server binding teammates inherit on clone.
.regent/*
!.regent/config.toml
!.regent/.gitignore`

// narrowRegentExclusion rewrites a project's root .gitignore so the server
// binding is reachable, and reports whether it changed anything.
//
// A root-level `.regent/` excludes the directory, and git does not descend into
// an excluded directory. The .regent/.gitignore that re-includes config.toml is
// therefore never read, and the previous attempt to share the binding through
// that nested file could not have worked — verified against real repositories.
// Narrowing the pattern to the directory's *contents* is what makes any
// re-inclusion apply at all.
//
// Only the exact whole-directory spellings are touched. A project that wrote
// something more specific meant something more specific, and silently rewriting
// it would be a worse failure than the one being fixed.
func narrowRegentExclusion(projectRoot string) (bool, error) {
	path := filepath.Join(projectRoot, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		// No root .gitignore is not a problem to solve: nothing is excluding the
		// binding, so nothing needs narrowing.
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	lines := strings.Split(string(data), "\n")
	changed := false
	for i, line := range lines {
		if regentExclusionLines[strings.TrimSpace(line)] {
			lines[i] = narrowedRegentExclusion
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
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
			isProject: true, wired: isConnected(m.root),
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
			projects = append(projects, pickerEntry{path: full, label: name, isProject: true, wired: isConnected(full)})
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
