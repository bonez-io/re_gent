package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/regent-vcs/regent/internal/style"
)

// This file wires re_gent into `git push`. See docs/rfcs/0002-git-push-integration.md.
//
// The shape of the problem: Git has no post-push hook, so `pre-push` is the only
// client-side moment tied to a push, and it comes with one dangerous property —
// a non-zero exit aborts the push. Everything below exists to use the moment
// while neutralising the property. The script re_gent writes exits 0 no matter
// what; a Regent failure may never become a Git failure, which is the same rule
// capture already lives by ("never break the user's agent") restated for Git.

const (
	// gitHookName is the Git hook we install into. Not configurable: it is the
	// only hook whose meaning ("this work is about to be shared") matches what
	// sync does.
	gitHookName = "pre-push"

	// gitHookPrevSuffix is appended to a hook that was already there. The user's
	// file is preserved byte for byte under this name and run first, so a hook
	// that used to abort pushes still aborts them.
	gitHookPrevSuffix = ".pre-regent"

	// gitHookMarker identifies a hook file as ours. Only files carrying it are
	// ever rewritten or removed; a file without it is the user's and is moved
	// aside, never edited.
	gitHookMarker = "# >>> re_gent pre-push >>>"

	// gitHookVerb is the hidden subcommand the script invokes. It is what makes
	// the script recognisable as ours regardless of the binary's filename, the
	// same discipline regentHookVerbs applies to agent hooks.
	gitHookVerb = "git-hook"

	// gitHookOptOutEnv disables the sync at push time without touching the
	// hook. Mirrors REGENT_SERVER_URL="" — the documented kill-switch pattern.
	gitHookOptOutEnv = "REGENT_GIT_SYNC_ON_PUSH"
)

// gitHookOutcome says what wiring the Git hook did, so callers can report the
// truth rather than the intent — the same lesson hookOutcome carries.
type gitHookOutcome struct {
	// Path is the hook file written, empty when nothing was.
	Path string
	// Skipped names why nothing was written, empty when Path is set. It is
	// advice for the user, not an error: a repository with no .git, or one
	// managed by a hooks manager, is a fine place to run rgt.
	Skipped string
	// Chained is true when a pre-existing hook was preserved and will run first.
	Chained bool
}

// wireGitHook installs the pre-push hook for projectRoot.
//
// It sits at the decision layer beside wireAgents — called from configureHooks
// and connectWireHooks — rather than inside it. wireAgents answers "which
// agents were wired", and its return value decides whether `rgt init` reports
// success; the Git hook is not an agent, and a project with no agent installed
// should still hear "no agent hooks were configured". Two questions, two
// answers, one call site each.
//
// It never returns an error for the ordinary reasons a hook cannot be written:
// no Git repository, a hooks manager owning the directory, or opt-out. Those
// come back in Skipped and are printed. Only a real filesystem failure — the
// directory exists but cannot be written — is an error, because that is the
// one case where the user asked for something and did not get it.
func wireGitHook(projectRoot string) (gitHookOutcome, error) {
	if optedOutOfGitHook(os.LookupEnv) {
		return gitHookOutcome{Skipped: gitHookOptOutEnv + "=0 is set"}, nil
	}
	hooksDir, reason := gitHooksDir(projectRoot)
	if reason != "" {
		return gitHookOutcome{Skipped: reason}, nil
	}
	outcome, err := installGitHook(hooksDir, hookBinary())
	if err != nil {
		return outcome, err
	}
	reportGitHookWired(outcome)
	return outcome, nil
}

// optedOutOfGitHook reads the kill switch. Only the literal "0" disables;
// anything else, including unset, keeps the default on. A typo therefore fails
// towards syncing, which is the direction the data prefers.
func optedOutOfGitHook(lookup func(string) (string, bool)) bool {
	v, ok := lookup(gitHookOptOutEnv)
	return ok && strings.TrimSpace(v) == "0"
}

// gitHooksDir locates the directory Git will read hooks from for projectRoot,
// or explains why there is not one worth writing to.
//
// Three cases:
//
//   - `.git` is a directory: the ordinary checkout, hooks live in .git/hooks.
//   - `.git` is a file: a worktree or submodule. It holds "gitdir: <path>", and
//     Git reads hooks from the *common* directory shared by all worktrees, named
//     by a `commondir` file inside gitdir. Writing to the worktree's own gitdir
//     would install a hook Git never runs.
//   - `core.hooksPath` is set: a hooks manager (husky, lefthook) owns hooks and
//     Git ignores .git/hooks entirely. Writing there would be dead code. We
//     report what we found and where the one line belongs instead.
//
// The core.hooksPath check shells out to `git config`. That is a read; RFC 0002
// forbids Git *writes*, and this is the only Git invocation in the file. If git
// is not on PATH the check is skipped and .git/hooks is used, which is what Git
// itself would do without configuration.
func gitHooksDir(projectRoot string) (dir string, skipReason string) {
	dotGit := filepath.Join(projectRoot, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", "not a Git repository (no .git in " + projectRoot + ")"
	}

	if custom := gitConfiguredHooksPath(projectRoot); custom != "" {
		return "", fmt.Sprintf(
			"core.hooksPath is set to %s — a hooks manager owns this repository's hooks. Add `rgt %s pre-push` to its pre-push stage to sync on push",
			custom, gitHookVerb)
	}

	gitDir := dotGit
	if !info.IsDir() {
		gitDir, err = resolveGitFile(dotGit)
		if err != nil {
			return "", ".git is a file but does not name a gitdir: " + err.Error()
		}
	}
	// A worktree's gitdir carries a `commondir` pointer to the shared .git.
	// The ordinary case has no such file and commondir is gitDir itself.
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common := strings.TrimSpace(string(data))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		gitDir = filepath.Clean(common)
	}
	return filepath.Join(gitDir, "hooks"), ""
}

// resolveGitFile reads a `.git` file of the form "gitdir: <path>".
func resolveGitFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", errors.New("missing gitdir: prefix")
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target), nil
}

// gitConfiguredHooksPath returns core.hooksPath for the repository at root, or
// "" when unset or when git cannot be asked.
func gitConfiguredHooksPath(root string) string {
	git, err := exec.LookPath("git")
	if err != nil {
		return ""
	}
	out, err := exec.Command(git, "-C", root, "config", "--get", "core.hooksPath").Output()
	if err != nil {
		return "" // exit 1 means unset; anything else we treat the same way
	}
	return strings.TrimSpace(string(out))
}

// installGitHook writes the pre-push hook into hooksDir, preserving whatever
// was there.
//
// Three states of the target file, three actions:
//
//   - Absent: write ours.
//   - Present and ours (carries the marker): rewrite it, so a binary that moved
//     is re-embedded. The preserved previous hook, if any, is left alone.
//   - Present and not ours: move it to <name>.pre-regent, then write ours,
//     which runs the moved file first. If a .pre-regent already exists as well,
//     refuse: two candidate previous hooks means a state we did not create,
//     and choosing which one to keep would destroy the other.
//
// The chained hook is run by path rather than spliced into our script. Splicing
// is fragile in every direction — a `set -e`, an early `exit`, a shebang other
// than sh — and each of those would silently drop our block. Preserving the
// user's file untouched and invoking it is what lets `rgt disconnect` restore
// it byte for byte.
func installGitHook(hooksDir, binary string) (gitHookOutcome, error) {
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return gitHookOutcome{}, fmt.Errorf("create %s: %w", hooksDir, err)
	}
	hookPath := filepath.Join(hooksDir, gitHookName)
	prevPath := hookPath + gitHookPrevSuffix

	existing, err := os.ReadFile(hookPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// nothing to preserve
	case err != nil:
		return gitHookOutcome{}, fmt.Errorf("read %s: %w", hookPath, err)
	case isRegentGitHook(existing):
		// ours already; fall through to rewrite
	default:
		if pathExists(prevPath) {
			return gitHookOutcome{}, fmt.Errorf(
				"both %s and %s exist and neither is re_gent's; refusing to guess which to keep — move one aside and re-run",
				hookPath, prevPath)
		}
		if err := os.Rename(hookPath, prevPath); err != nil {
			return gitHookOutcome{}, fmt.Errorf("preserve existing hook: %w", err)
		}
	}

	script := gitHookScript(binary)
	if err := writeExecutable(hookPath, script); err != nil {
		return gitHookOutcome{}, err
	}
	return gitHookOutcome{Path: hookPath, Chained: pathExists(prevPath)}, nil
}

// removeGitHook reverses installGitHook. It touches only a hook we wrote: a
// pre-push that is not ours is left exactly where it is. If we had preserved a
// previous hook, it is moved back into place, so the repository ends up in the
// state it was in before `rgt` ever ran there.
//
// Returns whether anything was removed, so callers can report truthfully.
func removeGitHook(projectRoot string) (bool, error) {
	hooksDir, reason := gitHooksDir(projectRoot)
	if reason != "" {
		return false, nil
	}
	hookPath := filepath.Join(hooksDir, gitHookName)
	prevPath := hookPath + gitHookPrevSuffix

	existing, err := os.ReadFile(hookPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", hookPath, err)
	}
	if !isRegentGitHook(existing) {
		return false, nil
	}
	if err := os.Remove(hookPath); err != nil {
		return false, fmt.Errorf("remove %s: %w", hookPath, err)
	}
	if pathExists(prevPath) {
		if err := os.Rename(prevPath, hookPath); err != nil {
			return true, fmt.Errorf("restore previous hook: %w", err)
		}
	}
	return true, nil
}

// isRegentGitHook reports whether hook content is one we wrote.
func isRegentGitHook(content []byte) bool {
	return bytes.Contains(content, []byte(gitHookMarker))
}

// gitHookScript renders the pre-push script for a given binary.
//
// Reading it top to bottom is the specification of the hook's runtime
// behaviour, so the shape is worth stating:
//
//  1. Run the preserved previous hook first, with the refs Git passed on stdin
//     and the same arguments, and propagate a non-zero exit. That hook keeps
//     every power it had.
//  2. Unless opted out, invoke `rgt git-hook pre-push`. The absolute path is
//     tried first (hooks run in Git's environment, not the installing shell's —
//     see resolveHookBinary), then PATH, then nothing. A machine that never
//     installed rgt gets a hook that does nothing, silently, which is correct.
//  3. exit 0. Unconditionally. rgt's own exit code is discarded on purpose; the
//     subcommand also always exits 0, so this is belt and braces against a
//     future regression there.
//
// stdin is redirected from /dev/null for the rgt call. Git passes the pushed
// refs on stdin, and RFC 0002 says they are to be discarded: Regent session
// refs have no correlation with Git branches, and reading them would tempt one
// into inventing it. Redirecting also guarantees the call can never block on a
// terminal.
func gitHookScript(binary string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(gitHookMarker + "\n")
	b.WriteString("# Installed by re_gent. Runs any pre-push hook that was here before, then\n")
	b.WriteString("# delivers queued re_gent history to the server. It never fails a push.\n")
	b.WriteString("# Remove with `rgt disconnect`; bypass once with `git push --no-verify`;\n")
	b.WriteString("# disable with " + gitHookOptOutEnv + "=0. See docs/rfcs/0002-git-push-integration.md.\n")
	// ${0%/*} is dirname in pure parameter expansion. `dirname` is an external
	// command, and this script must not depend on PATH — the whole reason the
	// binary path is embedded — so it must not depend on PATH for its own
	// plumbing either. Git invokes hooks by full path, so $0 always has a slash.
	b.WriteString(`prev="${0%/*}/` + gitHookName + gitHookPrevSuffix + `"` + "\n")
	b.WriteString(`if [ -x "$prev" ]; then` + "\n")
	b.WriteString(`  "$prev" "$@" || exit $?` + "\n")
	b.WriteString("fi\n")
	b.WriteString(`if [ "${` + gitHookOptOutEnv + `:-1}" != "0" ]; then` + "\n")
	args := gitHookVerb + " " + gitHookName + ` "$@" </dev/null`
	if filepath.IsAbs(binary) {
		q := shellQuote(binary)
		b.WriteString("  if [ -x " + q + " ]; then " + q + " " + args + "\n")
		b.WriteString("  elif command -v rgt >/dev/null 2>&1; then rgt " + args + "\n")
		b.WriteString("  fi\n")
	} else {
		b.WriteString("  if command -v rgt >/dev/null 2>&1; then rgt " + args + "; fi\n")
	}
	b.WriteString("fi\n")
	b.WriteString("exit 0\n")
	b.WriteString("# <<< re_gent pre-push <<<\n")
	return b.String()
}

// writeExecutable writes content to path with the executable bit set, via a
// temp file and rename so a crash never leaves a half-written hook that Git
// would then try to run.
func writeExecutable(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after a successful rename
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}

// reportGitHookWired names the file, like reportWired does for agents, and
// says when a previous hook was kept — that is the one thing a user with an
// existing hook will want to know.
func reportGitHookWired(o gitHookOutcome) {
	if o.Path == "" {
		return
	}
	fmt.Printf("  %s Git pre-push hook configured\n", style.Success(""))
	Verbosef(os.Stdout, "    path: %s\n", o.Path)
	if o.Chained {
		fmt.Printf("    your existing pre-push hook was kept and still runs first\n")
		Verbosef(os.Stdout, "    previous hook: %s\n",
			filepath.Base(o.Path)+gitHookPrevSuffix)
	}
}

// reportGitHookSkipped prints why no hook was written. Dim, not a warning:
// most reasons are the user's own configuration, and none of them stop capture.
func reportGitHookSkipped(o gitHookOutcome) {
	if o.Skipped == "" {
		return
	}
	Verbosef(os.Stdout, "  %s Git pre-push hook not configured: %s\n", style.DimText("-"), o.Skipped)
}

// gitHookFinding is doctor's view of the pre-push hook. The bool is false when
// there is nothing to say — the same rule diagnose applies to agents: a check
// on something that is not present here is noise, and noise is what makes
// people stop reading the output.
//
// It is advisory (severityWarning) in every non-OK case. Capture does not
// depend on it: a project without the hook records everything and delivers on
// every agent turn; it merely does not also deliver on `git push`. Failing
// doctor over it would abort the installer, and the installer had already done
// everything it needed to by the time this ran.
//
// Not a Git repository → nothing to report. There is no push to sync on.
// Hooks manager owns hooks → warning; the user has Git and a push, and the
// line to add is named. Absent → warning naming the wiring command. Foreign
// file in the way → warning saying wiring keeps it and runs it first.
func gitHookFinding(projectRoot string) (doctorFinding, bool) {
	const name = "git push sync"

	if !pathExists(filepath.Join(projectRoot, ".git")) {
		return doctorFinding{}, false
	}
	if optedOutOfGitHook(os.LookupEnv) {
		return doctorFinding{Name: name, OK: true,
			Detail: "disabled by " + gitHookOptOutEnv + "=0"}, true
	}
	hooksDir, reason := gitHooksDir(projectRoot)
	if reason != "" {
		return doctorFinding{Name: name, OK: false, Severity: severityWarning, Detail: reason}, true
	}
	hookPath := filepath.Join(hooksDir, gitHookName)
	content, err := os.ReadFile(hookPath)
	if errors.Is(err, fs.ErrNotExist) {
		return doctorFinding{Name: name, OK: false, Severity: severityWarning,
			Detail: "no pre-push hook; queued history is delivered on agent turns but not on git push — run rgt connect (or rgt init) to wire it"}, true
	}
	if err != nil {
		return doctorFinding{Name: name, OK: false, Severity: severityWarning,
			Detail: fmt.Sprintf("cannot read %s: %v", hookPath, err)}, true
	}
	if !isRegentGitHook(content) {
		return doctorFinding{Name: name, OK: false, Severity: severityWarning,
			Detail: fmt.Sprintf("%s exists but is not re_gent's; rgt connect keeps it and runs it first", hookPath)}, true
	}
	detail := hookPath
	if pathExists(hookPath + gitHookPrevSuffix) {
		detail += " (chains your previous pre-push hook)"
	}
	return doctorFinding{Name: name, OK: true, Detail: detail}, true
}
