package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/remotetest"
	"github.com/bonez-io/re_gent/internal/store"
)

func writeCliTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// TestRunWorkspaceSyncLocalMode covers the plain, no-server-configured path:
// runWorkspaceSync must write the baseline into the project's own .regent/
// store, the same one a local-mode hook would write agent steps into.
func TestRunWorkspaceSyncLocalMode(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	writeCliTestFile(t, root, "a.txt", "hello\n")
	writeCliTestFile(t, root, "nested/b.txt", "world\n")

	res, err := runWorkspaceSync(root)
	if err != nil {
		t.Fatalf("runWorkspaceSync: %v", err)
	}
	if !res.Wrote || res.FileCount != 2 || res.StepHash == "" {
		t.Fatalf("result = %+v, want wrote=true fileCount=2 with a step hash", res)
	}

	s, err := store.Open(filepath.Join(root, ".regent"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ref, err := s.ReadRef("sync/workspace")
	if err != nil {
		t.Fatalf("read sync ref: %v", err)
	}
	if string(ref) != res.StepHash {
		t.Fatalf("refs/sync/workspace = %s, want %s", ref, res.StepHash)
	}

	// Calling it again with nothing changed must be a no-op.
	res2, err := runWorkspaceSync(root)
	if err != nil {
		t.Fatalf("second runWorkspaceSync: %v", err)
	}
	if res2.Wrote {
		t.Fatal("second call with no changes reported wrote=true")
	}
}

// TestRunWorkspaceSyncNoRepoErrors asserts the local-mode fallback fails
// clearly rather than silently, when there is no .regent/ to write into —
// distinct from a git hook's own error handling (which must never surface an
// error to the user), this is the direct call any caller that does want to
// know about a failure goes through.
func TestRunWorkspaceSyncNoRepoErrors(t *testing.T) {
	root := t.TempDir()
	_, err := runWorkspaceSync(root)
	if err == nil {
		t.Fatal("expected an error with no .regent/ present")
	}
	if !strings.Contains(err.Error(), "not a re_gent repository") {
		t.Errorf("error = %v, want it to name the missing repository", err)
	}
}

// TestSyncWorkspaceCommandReportsBaselineSize covers `rgt sync --workspace`
// end to end through the cobra command in local mode.
func TestSyncWorkspaceCommandReportsBaselineSize(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	writeCliTestFile(t, root, "a.txt", "hello\n")
	withWorkingDir(t, root)

	var out bytes.Buffer
	cmd := SyncCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--workspace"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("rgt sync --workspace: %v", err)
	}
	if !strings.Contains(out.String(), "Workspace baseline updated: 1 file(s)") {
		t.Fatalf("output = %q, want it to report the baseline size", out.String())
	}

	out.Reset()
	cmd = SyncCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--workspace"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("second rgt sync --workspace: %v", err)
	}
	if !strings.Contains(out.String(), "Workspace baseline unchanged: 1 file(s)") {
		t.Fatalf("output = %q, want it to report no change", out.String())
	}
}

// TestInitPrintsBaselineSnapshot pins issue #106's first-run contract: rgt
// init takes a baseline snapshot right after hooks are wired and reports its
// size, even with --skip-hook (the baseline is not conditional on hook
// wiring — see the comment above runBaselineSync's call site in init.go).
func TestInitPrintsBaselineSnapshot(t *testing.T) {
	root := t.TempDir()
	writeCliTestFile(t, root, "a.txt", "hello\n")
	writeCliTestFile(t, root, "b.txt", "world\n")
	withWorkingDir(t, root)

	var out bytes.Buffer
	cmd := InitCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--skip-hook"})
	_ = cmd.Execute() // exit status reflects hook wiring, not this assertion

	if !strings.Contains(out.String(), "Baseline snapshot: 2 file(s)") {
		t.Fatalf("init output = %q, want a baseline snapshot line reporting 2 files", out.String())
	}

	s, err := store.Open(filepath.Join(root, ".regent"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := s.ReadRef("sync/workspace"); err != nil {
		t.Fatalf("read sync ref after init: %v", err)
	}
}

// TestSyncWorkspaceCommandDeliversInServerMode is the regression test for the
// bug that shipped once, and only once, in this feature's development: an
// earlier version delivered the workspace-sync step through the same
// automatic queue every agent turn drains (Spool.Status/Flush), which made
// two ordinary machines' independently-generated baselines "diverge" and
// silently absorbed the delivery report `rgt sync --workspace` was supposed
// to print. The fix is that runWorkspaceSync never delivers on its own —
// runSyncWorkspace explicitly pushes exactly the sync ref afterward, the same
// way `rgt sync <ref>` pushes any other named ref. This asserts the user-
// visible result: one command, one report, delivered.
func TestSyncWorkspaceCommandDeliversInServerMode(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)

	root := t.TempDir()
	writeCliTestFile(t, root, "a.txt", "hello\n")
	withWorkingDir(t, root)

	t.Setenv("REGENT_SERVER_URL", srv.URL())
	t.Setenv("REGENT_REPO_ID", "workspace-e2e-repo")
	t.Setenv("REGENT_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))

	var out bytes.Buffer
	cmd := SyncCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--workspace"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("rgt sync --workspace: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "Workspace baseline updated: 1 file(s)") {
		t.Fatalf("output = %q, want the baseline line", got)
	}
	if !strings.Contains(got, "delivered") {
		t.Fatalf("output = %q, want a delivery line (the explicit push runSyncWorkspace makes)", got)
	}
	if srv.Ref("sync/workspace") == "" {
		t.Fatal("server never received the workspace-sync ref")
	}
}
