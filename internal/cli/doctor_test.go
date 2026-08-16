package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	broken := []doctorFinding{
		{Name: "repository", OK: true},
		{Name: "claude hooks", OK: false, Detail: "no re_gent hook found"},
	}
	if allOK(broken) {
		t.Error("allOK true despite a failing finding; doctor would exit 0 on a broken install")
	}
}

// Git identity is the only thing naming who ran a session. When it is unset,
// every step is recorded anonymously and nothing says so at the time — the loss
// is only discovered later, when the history that would have proved authorship
// is already written. Doctor is the one place that can say it while it still
// costs one command to fix.
func TestDiagnoseFailsWhenGitIdentityIsUnset(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".regent"))
	withoutGitIdentity(t, root)

	findings := diagnose(root)

	f := findFinding(t, findings, "git identity")
	if f.OK {
		t.Errorf("git identity reported healthy with no identity configured: %s", f.Detail)
	}
	if allOK(findings) {
		t.Error("diagnose exited healthy while every captured step would be anonymous")
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
