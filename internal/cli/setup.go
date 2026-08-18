package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/regent-vcs/regent/internal/config"
	"github.com/regent-vcs/regent/internal/store"
	"github.com/regent-vcs/regent/internal/style"
)

// This file is what is left of `rgt setup`: the server binding a machine
// remembers, and the offer to commit a project's wiring so teammates inherit it
// on clone. The command itself is a tombstone in cmd/rgt, and the picker it
// opened is gone (#28).

// resolveServerURL answers "which server" from the argument, else from the
// server this machine already connected to.
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
//
// The picker asked this to label its rows and to decide whether marking one
// meant connect or disconnect; with the picker gone (#28) the callers left are
// the tests, where it is the oracle for "is this project wired". It stays
// because that question needs one answer, and a test that spelled the answer
// out itself would be free to drift from the one connect and disconnect use.
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
	cfg, err := readRepoConfig(dir)
	if err != nil {
		return store.RemoteConfig{}, err
	}
	return cfg.Remote, nil
}

// readRepoConfig reads the complete portable project binding. Keep this next
// to readRemoteConfig: the remote and capture declarations deliberately share
// .regent/config.toml and must be parsed with the same rules.
func readRepoConfig(dir string) (store.RepoConfig, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".regent", "config.toml"))
	if err != nil {
		return store.RepoConfig{}, err
	}
	var cfg store.RepoConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return store.RepoConfig{}, err
	}
	return cfg, nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
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

// offerShareTUI is the terminal-native form of offerShare. The ordinary reader
// version remains the accessibility and redirected-input fallback; connect
// reaches this version only after proving stdin is a person at a terminal.
func offerShareTUI(projects []string, in io.Reader, out io.Writer) {
	for _, p := range projects {
		if !isDir(filepath.Join(p, ".git")) {
			continue
		}
		yes, err := style.Confirm(
			in,
			out,
			"Share this setup with your team?",
			"Commits the re_gent binding and Claude hooks. Never pushes.",
		)
		if err != nil {
			fmt.Fprintf(out, "  confirmation unavailable: %v\n", err)
			return
		}
		flow := style.NewFlow(out)
		if !yes {
			flow.Hint("Team sharing skipped — you can commit the wiring later")
			continue
		}
		if err := flow.Run("Committing team wiring", func() error { return commitWiring(p) }); err != nil {
			flow.Warning("The project is connected, but its wiring was not committed")
			Verbosef(out, "  %v\n", err)
			continue
		}
		flow.Hint("No push was performed")
	}
}

// readAnswer reads one typed line, ending at EITHER newline or carriage return.
// That distinction is the whole point: Enter does not always arrive as \n. A
// Windows terminal sends \r\n, and a terminal handed back by a full-screen
// program with ICRNL cleared sends a bare \r. A reader waiting only for \n then
// blocks forever while the keystroke echoes — indistinguishable, from the
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
