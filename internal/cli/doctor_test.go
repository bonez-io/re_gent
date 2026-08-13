package cli

import (
	"os"
	"path/filepath"
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
