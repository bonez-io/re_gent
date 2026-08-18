package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regent-vcs/regent/internal/hook"
	"github.com/regent-vcs/regent/internal/store"
)

// rgt doctor is the verification step. It exists because setup can succeed
// mechanically and still capture nothing: the hook file is written but never
// fires, and rgt status/log/sessions all exit 0 without mentioning it. On a
// team the person who ran the install is not the person who would notice.
//
// Doctor is local-only. It reads config on this machine and reports to this
// user; it sends nothing anywhere.

func TestDiagnoseReportsMissingRepository(t *testing.T) {
	root := t.TempDir()

	findings := diagnose(root)

	f := findFinding(t, findings, "repository")
	if f.OK {
		t.Error("repository reported healthy with no .regent/ directory")
	}
	if allOK(findings) {
		t.Error("diagnose reported everything healthy in an uninitialized directory")
	}
}

func TestDiagnoseReportsHooksMissingWhenNothingIsWired(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".regent"))
	// A .claude directory with no re_gent hook in it: the exact shape left
	// behind when init claimed success but wired nothing.
	mustMkdir(t, filepath.Join(root, ".claude"))
	mustWrite(t, filepath.Join(root, ".claude", "settings.json"), `{"hooks":{}}`)

	findings := diagnose(root)

	f := findFinding(t, findings, "claude hooks")
	if f.OK {
		t.Error("claude hooks reported healthy when settings.json contains no re_gent hook")
	}
}

func TestDiagnoseReportsHooksHealthyAfterWiring(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".regent"))

	if _, err := wireAgents(root, []agentTarget{agentClaude}); err != nil {
		t.Fatalf("wireAgents: %v", err)
	}

	findings := diagnose(root)

	f := findFinding(t, findings, "claude hooks")
	if !f.OK {
		t.Errorf("claude hooks reported unhealthy immediately after wireAgents: %s", f.Detail)
	}
}

// The install script's last line is `rgt doctor`. If a broken install exits 0,
// the whole verification step is decorative.
func TestDoctorFailsWhenAnyFindingFails(t *testing.T) {
	healthy := []doctorFinding{{Name: "repository", OK: true}}
	if !allOK(healthy) {
		t.Error("allOK false for an all-healthy set")
	}
	if hasFailures(healthy) {
		t.Error("hasFailures true for an all-healthy set; doctor would exit non-zero on a good install")
	}

	broken := []doctorFinding{
		{Name: "repository", OK: true},
		{Name: "claude hooks", OK: false, Detail: "no re_gent hook found"},
	}
	if allOK(broken) {
		t.Error("allOK true despite a failing finding")
	}
	if !hasFailures(broken) {
		t.Error("hasFailures false despite a hook finding that failed; doctor would exit 0 on a broken install")
	}
}

// Severity is not something a check gets to leave unsaid. A new finding that
// forgets to classify itself must block, because the cost of wrongly warning
// about a broken install is a project that captures nothing and says it is
// fine, while the cost of wrongly failing on a warning is a spurious error.
func TestAFindingThatSaysNothingAboutSeverityBlocks(t *testing.T) {
	unclassified := []doctorFinding{{Name: "some new check", OK: false, Detail: "went wrong"}}
	if !hasFailures(unclassified) {
		t.Error("a finding with no severity set did not block; the safe default has to be failure")
	}
}

// Git identity is the only thing naming who ran a session. When it is unset,
// every step is recorded anonymously and nothing says so at the time — the loss
// is only discovered later, when the history that would have proved authorship
// is already written. Doctor is the one place that can say it while it still
// costs one command to fix.
//
// It says it as a warning. This check used to be fatal, which made doctor exit
// non-zero, which aborted `curl … | sh` installs at their last step on exactly
// the machines least likely to have an identity configured. Capture works
// without one; unwinding a working install over it does not.
func TestDiagnoseWarnsButDoesNotFailWhenGitIdentityIsUnset(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".regent"))
	withoutGitIdentity(t, root)
	if _, err := wireAgents(root, []agentTarget{agentClaude}); err != nil {
		t.Fatalf("wireAgents: %v", err)
	}
	// An empty PATH so a claude or codex binary installed on the machine
	// running this suite cannot add a hook finding of its own and decide the
	// answer. The only thing left to be unhappy about is the identity.
	t.Setenv("PATH", "")

	findings := diagnose(root)

	f := findFinding(t, findings, "git identity")
	if f.OK {
		t.Errorf("git identity reported healthy with no identity configured: %s", f.Detail)
	}
	if allOK(findings) {
		t.Error("diagnose reported nothing at all while every captured step would be anonymous")
	}
	if f.Severity != severityWarning {
		t.Error("an unset git identity is fatal to doctor's exit code, so an install that already succeeded will be reported as failed")
	}
	if hasFailures(findings) {
		t.Error("diagnose found a blocking failure on a machine whose only problem is an unset identity")
	}
}

func TestDiagnoseReportsGitIdentityHealthyWhenConfigured(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".regent"))
	withoutGitIdentity(t, root)
	t.Setenv("REGENT_AUTHOR_NAME", "Ada Lovelace")
	t.Setenv("REGENT_AUTHOR_EMAIL", "ada@example.com")

	f := findFinding(t, diagnose(root), "git identity")
	if !f.OK {
		t.Errorf("git identity reported unhealthy with an identity configured: %s", f.Detail)
	}
	if !strings.Contains(f.Detail, "Ada Lovelace") {
		t.Errorf("git identity detail = %q, want it to name the identity in use", f.Detail)
	}
}

// Doctor answers "who will these steps be attributed to". The hook path that
// actually writes steps used to never ask: it built the step with no Author at
// all, so on a machine where doctor reported identity as healthy every recorded
// step was anonymous, permanently, and nothing said so.
//
// The identity here is set through REGENT_AUTHOR_NAME, which only
// capture.ResolveAuthor knows how to read. A writer that resolved identity by
// any other route — shelling out to git config itself, reading a different
// variable — would disagree with doctor here and fail this test. That is the
// point: these two must stay one lookup, not two that can drift.
func TestRecordedStepCarriesTheIdentityDoctorReports(t *testing.T) {
	workspace := t.TempDir()
	withoutGitIdentity(t, workspace)
	t.Setenv("REGENT_AUTHOR_NAME", "Ada Lovelace")
	t.Setenv("REGENT_AUTHOR_EMAIL", "ada@example.com")

	finding := identityFinding()
	if !finding.OK {
		t.Fatalf("doctor found no identity, so there are not two halves to compare: %s", finding.Detail)
	}

	step := recordStepThroughHook(t, workspace, "identity-session")

	if got := formatAuthor(step.Author); got != finding.Detail {
		t.Errorf("step was recorded as %q but doctor reports %q; the writer and the check read identity from different places", got, finding.Detail)
	}
}

// The other direction of the same agreement, and the reason this is not fixed
// by inventing an author: with nothing configured, doctor says so and the step
// carries no author. Filling in a hostname or a login here would make doctor's
// warning a lie and put a name on work nobody claimed.
func TestRecordedStepHasNoAuthorWhenDoctorFindsNoIdentity(t *testing.T) {
	workspace := t.TempDir()
	withoutGitIdentity(t, workspace)

	if finding := identityFinding(); finding.OK {
		t.Fatalf("identity isolation failed; doctor still sees %q", finding.Detail)
	}

	step := recordStepThroughHook(t, workspace, "anonymous-session")

	if step.Author.Name != "" || step.Author.Email != "" {
		t.Errorf("step recorded author %q with no identity configured; doctor promised none", formatAuthor(step.Author))
	}
}

// recordStepThroughHook drives the real hook entry point over a fresh workspace
// and returns the step it wrote, so tests assert on what was persisted rather
// than on what the code path looked like on the way there.
func recordStepThroughHook(t *testing.T, workspace, sessionID string) *store.Step {
	t.Helper()

	s, err := store.Init(workspace)
	if err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "touched.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("seed workspace file: %v", err)
	}

	var stdin bytes.Buffer
	payload := hook.Payload{
		SessionID:    sessionID,
		ToolUseID:    "tool_write_1",
		ToolName:     "Write",
		ToolInput:    json.RawMessage(`{"file_path":"touched.txt"}`),
		ToolResponse: json.RawMessage(`{"success":true}`),
		CWD:          workspace,
	}
	if err := json.NewEncoder(&stdin).Encode(payload); err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	if err := hook.Run(&stdin, io.Discard); err != nil {
		t.Fatalf("hook.Run: %v", err)
	}

	head, err := s.ReadRef("sessions/" + sessionID)
	if err != nil {
		t.Fatalf("read session ref: %v", err)
	}
	step, err := s.ReadStep(head)
	if err != nil {
		t.Fatalf("read step %s: %v", head, err)
	}
	return step
}

// withoutGitIdentity makes identity resolution deterministic: no environment
// override, and no global or system git config for the subprocess to read. The
// working directory moves to a temp dir so there is no repository-local config
// above it either.
func withoutGitIdentity(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("REGENT_AUTHOR_NAME", "")
	t.Setenv("REGENT_AUTHOR_EMAIL", "")
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	withWorkingDir(t, dir)
}

func findFinding(t *testing.T, findings []doctorFinding, name string) doctorFinding {
	t.Helper()
	for _, f := range findings {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no finding named %q in %v", name, findings)
	return doctorFinding{}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// Doctor decides whether a hook is ours by looking at the binary's filename,
// which means it only recognises re_gent when re_gent is called "rgt". Any
// other name — a side-by-side dev build, a versioned copy, a rename — and the
// check reports "nothing will be captured" over a project that is capturing
// normally. Observed on a real machine: hooks installed, steps recorded, blame
// answering correctly, doctor insisting none of it was wired.
//
// What identifies the hook is not the filename but the subcommand. Nothing
// except re_gent is invoked with `tool-batch-hook`, and re_gent writes it
// whatever the binary is called.
func TestClaudeHookIsRecognisedWhateverTheBinaryIsCalled(t *testing.T) {
	for _, binary := range []string{
		"rgt",                           // the default
		"/Users/dev/.local/bin/rgt-dev", // a build kept beside a release
		"/opt/regent/bin/regent",        // the long name, absolute
	} {
		root := t.TempDir()
		mustMkdir(t, filepath.Join(root, ".claude"))
		mustWrite(t, filepath.Join(root, ".claude", "settings.json"), `{
  "hooks": {
    "PostToolUse": [
      {"hooks": [{"type": "command", "command": "`+binary+` tool-batch-hook"}]}
    ]
  }
}`)

		if finding := claudeHookFinding(root); !finding.OK {
			t.Errorf("hooks invoking %q are not recognised as re_gent's: %s", binary, finding.Detail)
		}
	}
}

// The user's exact situation, reported ten minutes after the one-line install
// (#27). Claude Code is kept open at ~/Documents/GitHub; the project is
// ~/Documents/GitHub/tsenta-agent. The install wired the project, doctor
// objected that the ancestor was unwired and advised wiring it, they did — and
// doctor then printed four green ticks over a project whose .regent/ had been
// empty ever since, because every step was landing in the ancestor's.
//
// Both directories are wired here. That is the state the old advice produced,
// and the state in which shadowingClaudeWorkspace returned "" — reading "the
// ancestor has a hook" as "this project is fine". They are different questions:
// capture resolves its store from the agent session's working directory
// (capture.Open opens cwd/.regent, with no upward walk), so a session opened at
// the ancestor records there and nothing this project can be told about its own
// settings.json changes that.
func TestDoctorIsNotHealthyWhenAWiredAncestorCapturesInstead(t *testing.T) {
	workspace := t.TempDir()
	mustMkdir(t, filepath.Join(workspace, ".claude"))
	mustWrite(t, filepath.Join(workspace, ".claude", "settings.json"), wiredClaudeSettings)
	mustMkdir(t, filepath.Join(workspace, ".regent"))

	project := filepath.Join(workspace, "tsenta-agent")
	mustMkdir(t, filepath.Join(project, ".claude"))
	mustWrite(t, filepath.Join(project, ".claude", "settings.json"), wiredClaudeSettings)
	mustMkdir(t, filepath.Join(project, ".regent"))

	finding := claudeHookFinding(project)

	if finding.OK {
		t.Errorf("doctor reports this project healthy, but an agent opened at %s records into %s and rgt log here stays empty: %s",
			workspace, filepath.Join(workspace, ".regent"), finding.Detail)
	}
	// Not healthy is half the promise. The other half is that the report says
	// where the work actually goes, which is the one fact that makes the empty
	// log explicable rather than mysterious.
	//
	// Asserted on the ancestor's .regent/ and not on the ancestor itself: the
	// project path has the ancestor path as a prefix, so merely naming the
	// project would satisfy a Contains check on it. Found by mutation — the
	// looser assertion survived a revert to the original bug.
	if !strings.Contains(finding.Detail, filepath.Join(workspace, ".regent")) {
		t.Errorf("doctor never names the directory the work would be recorded in: %s", finding.Detail)
	}
}

// The severity half, and it decides the installer's exit code: `curl … | sh`
// ends in `rgt doctor` and unwinds on non-zero.
//
// A wired ancestor is a warning, not a failure, because capture genuinely
// works — the steps are recorded, just at the ancestor and blended with every
// other project under it. Failing here would print "Nothing will be captured
// until those problems are fixed" over a machine where things are in fact being
// captured, and would abort an install that had already done everything an
// installer can do. The remaining move — opening the agent inside the project —
// is not one rgt can make.
func TestAWiredAncestorWarnsWithoutUnwindingTheInstall(t *testing.T) {
	workspace := t.TempDir()
	mustMkdir(t, filepath.Join(workspace, ".claude"))
	mustWrite(t, filepath.Join(workspace, ".claude", "settings.json"), wiredClaudeSettings)

	project := filepath.Join(workspace, "tsenta-agent")
	mustMkdir(t, filepath.Join(project, ".claude"))
	mustWrite(t, filepath.Join(project, ".claude", "settings.json"), wiredClaudeSettings)

	finding := claudeHookFinding(project)

	if finding.Severity != severityWarning {
		t.Errorf("a wired ancestor is fatal to doctor's exit code, so the one-line install aborts on a machine that is capturing: %s", finding.Detail)
	}
	if hasFailures([]doctorFinding{finding}) {
		t.Error("hasFailures true for a wired ancestor; the installer would report a working setup as ruined")
	}
}

// The other direction, and the reason the case above is only a warning: with
// the ancestor unwired, a session opened there loads its own settings.json,
// finds no re_gent hook, and captures nothing anywhere. That is total loss and
// must keep blocking.
// A project that is correctly wired must never be declared broken because of
// where an agent *might* be opened.
//
// Doctor cannot see which directory the user opens their agent in. It can only
// see that some ancestor has a .claude/ — true on any machine where an agent
// was ever started above this project, and true of a directory holding nothing
// but the settings.local.json Claude Code writes by itself. Turning that into
// "nothing will be captured" tells a user doing exactly the right thing that
// their setup is broken, and exits the one-line installer non-zero over a
// project that captures perfectly.
//
// That is the same "reports something other than what is true" failure the rest
// of this file exists to remove, pointed the other way. This replaced an
// earlier test asserting the opposite, written while the unwired ancestor was
// assumed to be where the agent runs.
//
// From a real install: the owner opens the agent inside the project, the
// project's own hook is present, its binary resolves — and the installer still
// ended "Setup ran, but verification failed. Nothing will be captured until
// those problems are fixed."
func TestAWiredProjectIsNotDeclaredBrokenBecauseAnAncestorMightBeOpened(t *testing.T) {
	workspace := t.TempDir()
	mustMkdir(t, filepath.Join(workspace, ".claude"))
	mustWrite(t, filepath.Join(workspace, ".claude", "settings.json"), `{"hooks":{}}`)

	project := filepath.Join(workspace, "tsenta-agent")
	mustMkdir(t, filepath.Join(project, ".claude"))
	mustWrite(t, filepath.Join(project, ".claude", "settings.json"), wiredClaudeSettings)

	finding := claudeHookFinding(project)

	if finding.Severity == severityFailure {
		t.Errorf("doctor blocks a correctly wired project over where an agent might be opened, which it cannot know; the one-line install exits non-zero saying nothing will be captured:\n%s", finding.Detail)
	}
	// It must still say something. The ancestor is a real hazard, just not a
	// verdict — and silence here is the original bug coming back.
	if finding.OK {
		t.Errorf("doctor says nothing at all about %s; a user who does open their agent there gets no warning", workspace)
	}
}

// A project owner can answer doctor's otherwise unknowable question once in
// the portable binding. This is intentionally scoped to the ancestor-layout
// advisory: the hook itself must still be valid before doctor reports green.
func TestDoctorAcceptsAnIntentionalProjectCaptureRoot(t *testing.T) {
	workspace := t.TempDir()
	mustMkdir(t, filepath.Join(workspace, ".claude"))
	mustWrite(t, filepath.Join(workspace, ".claude", "settings.json"), `{"hooks":{}}`)

	project := filepath.Join(workspace, "project")
	mustMkdir(t, filepath.Join(project, ".claude"))
	mustWrite(t, filepath.Join(project, ".claude", "settings.json"), wiredClaudeSettings)
	s, err := store.Init(project)
	if err != nil {
		t.Fatalf("init project store: %v", err)
	}
	if err := recordCaptureRoot(s, "project"); err != nil {
		t.Fatalf("record capture root: %v", err)
	}

	if finding := claudeHookFinding(project); !finding.OK {
		t.Errorf("intentional project capture root still warns: %s", finding.Detail)
	}
}

func TestInitRecordsCaptureRootInVisibleProjectBinding(t *testing.T) {
	project := t.TempDir()
	withWorkingDir(t, project)

	cmd := InitCmd()
	cmd.SetArgs([]string{"--skip-hook", "--capture-root", "project"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(project, ".regent", "config.toml"))
	if err != nil {
		t.Fatalf("read project binding: %v", err)
	}
	if !strings.Contains(string(data), "[capture]") || !strings.Contains(string(data), `root = 'project'`) {
		t.Errorf("capture root is not visible in project binding:\n%s", data)
	}
}

// A monorepo can deliberately bind its one capture root at the workspace.
// No descendant is silently blessed: doctor is green here because this is the
// directory with both the binding and the hook that captures work.
func TestDoctorAcceptsAnIntentionalWorkspaceCaptureRoot(t *testing.T) {
	workspace := t.TempDir()
	mustMkdir(t, filepath.Join(workspace, ".claude"))
	mustWrite(t, filepath.Join(workspace, ".claude", "settings.json"), wiredClaudeSettings)
	s, err := store.Init(workspace)
	if err != nil {
		t.Fatalf("init workspace store: %v", err)
	}
	if err := recordCaptureRoot(s, "workspace"); err != nil {
		t.Fatalf("record capture root: %v", err)
	}

	cfg, err := s.ReadRepoConfig()
	if err != nil {
		t.Fatalf("read project binding: %v", err)
	}
	if cfg.Capture.Root != "workspace" {
		t.Fatalf("capture root = %q, want workspace", cfg.Capture.Root)
	}
	if finding := claudeHookFinding(workspace); !finding.OK {
		t.Errorf("intentional workspace capture root is not healthy: %s", finding.Detail)
	}
}

// An acknowledgement is not a substitute for capture. In particular it must
// not turn a config file with no re_gent hook into a green doctor result.
func TestDoctorAcknowledgementNeverHidesMissingCapture(t *testing.T) {
	project := t.TempDir()
	mustMkdir(t, filepath.Join(project, ".claude"))
	mustWrite(t, filepath.Join(project, ".claude", "settings.json"), `{"hooks":{}}`)
	s, err := store.Init(project)
	if err != nil {
		t.Fatalf("init project store: %v", err)
	}
	if err := recordCaptureRoot(s, "project"); err != nil {
		t.Fatalf("record capture root: %v", err)
	}

	finding := claudeHookFinding(project)
	if finding.OK {
		t.Errorf("capture acknowledgement made an unwired project green: %s", finding.Detail)
	}
	if !hasFailures([]doctorFinding{finding}) {
		t.Error("missing hook became non-fatal after capture acknowledgement")
	}
}

// wiredClaudeSettings is the shape wireAgents leaves behind: a settings file
// whose hook invokes one of re_gent's subcommands. Spelled with the bare name
// on purpose — isRegentHookCommand identifies our hooks by the subcommand, not
// the binary's path, so this is as real a hook as an absolute one.
const wiredClaudeSettings = `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"rgt tool-batch-hook"}]}]}}`

// The other half of the same judgement, and the reason the check exists: a
// settings file full of somebody else's hooks must still report that nothing
// of ours is wired. Without this, "always OK" would satisfy the test above and
// doctor would go from lying one way to lying the other.
func TestClaudeHooksThatAreNotOursAreNotMistakenForOurs(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".claude"))
	mustWrite(t, filepath.Join(root, ".claude", "settings.json"), `{
  "hooks": {
    "PostToolUse": [
      {"hooks": [
        {"type": "command", "command": "prettier --write ."},
        {"type": "command", "command": "npm run lint-hook"}
      ]}
    ]
  }
}`)

	if finding := claudeHookFinding(root); finding.OK {
		t.Errorf("doctor claims re_gent is wired here, but nothing in this file invokes it: %s", finding.Detail)
	}
}
