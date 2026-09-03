package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/capture"
	"github.com/bonez-io/re_gent/internal/ignore"
	"github.com/bonez-io/re_gent/internal/server"
	"github.com/bonez-io/re_gent/internal/snapshot"
	"github.com/bonez-io/re_gent/internal/store"
)

// TestWorkspaceSyncBaselineServedByFilesAndBlameAPI is the acceptance test for
// issue #106: a workspace baseline taken before any agent step exists must be
// what /api/files serves, and an agent step recorded afterward must inherit
// the baseline's blame for everything it did not touch.
//
// It exercises the real pieces end to end — a git working tree,
// capture.WorkspaceSync computing the baseline exactly as `rgt sync
// --workspace` and the first-run hooks do, capture.ComputeAndWriteBlame
// seeding an agent step's blame from that baseline, and the real HTTP read
// API — rather than constructing steps by hand the way the read_api_test.go
// unit tests do.
func TestWorkspaceSyncBaselineServedByFilesAndBlameAPI(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	// 1. A git working tree with files already in it, the way a real project
	// looks the moment `rgt init` runs against existing history.
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "e2e@example.com")
	runGit(t, root, "config", "user.name", "e2e")
	writeTestFile(t, root, "untouched.txt", "line one\nline two\n")
	writeTestFile(t, root, "edited.txt", "before\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-q", "-m", "init")

	// 2. The server, and a repo registered on it. The repo's own store lives
	// at dataDir/repos/<id> (see internal/server.Server doc comment); pointing
	// the baseline sync directly at that directory is the same shape a
	// self-hosted deployment has when capture and the server share one disk,
	// and it lets this test exercise the real read API without also
	// re-proving the push pipeline, which internal/remote already covers.
	dataDir := t.TempDir()
	srv, err := server.New(dataDir)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	const repoID = "workspace-sync-e2e"
	if status := createTestRepo(t, ts, repoID); status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create repo: status %d", status)
	}
	repoStoreDir := filepath.Join(dataDir, "repos", repoID)
	s, err := store.Open(repoStoreDir)
	if err != nil {
		t.Fatalf("open repo store: %v", err)
	}

	// 3. Run the baseline — exactly what rgt init/connect/sync --workspace do.
	syncHash, wrote, fileCount, err := capture.WorkspaceSync(s, root)
	if err != nil {
		t.Fatalf("WorkspaceSync: %v", err)
	}
	if !wrote || fileCount != 2 {
		t.Fatalf("WorkspaceSync: wrote=%v fileCount=%d, want wrote=true fileCount=2", wrote, fileCount)
	}

	// 4. Before any agent step exists, /api/files (no params) must already
	// list the baseline's files, sourced from the sync step.
	var filesResp filesResponseE2E
	getJSON(t, ts.URL+"/"+repoID+"/api/files", &filesResp)
	if filesResp.Source != "sync" || filesResp.StepHash != string(syncHash) || filesResp.TotalFiles != 2 {
		t.Fatalf("files before any agent step = %+v, want source=sync step=%s total=2", filesResp, syncHash)
	}

	// 5. Record an agent step: it snapshots the whole working tree (the way
	// real capture does every turn, not just the files it touched), with one
	// file actually edited.
	writeTestFile(t, root, "edited.txt", "before\nafter\n")
	agentTree, err := snapshot.Snapshot(s, root, ignore.Default(root))
	if err != nil {
		t.Fatalf("snapshot agent tree: %v", err)
	}
	agentTS := time.Now().Add(time.Hour).UnixNano() // strictly after the sync step
	agentStep := &store.Step{
		Tree:           agentTree,
		SessionID:      "claude_code--sess1",
		Origin:         "claude_code",
		TimestampNanos: agentTS,
	}
	agentStepHash, err := s.WriteStep(agentStep)
	if err != nil {
		t.Fatalf("write agent step: %v", err)
	}
	// parentHash "" mirrors the first step of a brand-new session, which is
	// exactly the case that needs the baseline: without it, every unedited
	// line in the whole working tree would be misattributed to this step.
	if err := capture.ComputeAndWriteBlame(s, "", agentStepHash, agentTree); err != nil {
		t.Fatalf("ComputeAndWriteBlame: %v", err)
	}
	if err := s.UpdateRef("sessions/claude_code--sess1", "", agentStepHash); err != nil {
		t.Fatalf("update session ref: %v", err)
	}

	// 6. Now /api/files with no params must resolve to the agent's step: it
	// is strictly newer, and a session wins ties anyway.
	getJSON(t, ts.URL+"/"+repoID+"/api/files", &filesResp)
	if filesResp.Source != "session" || filesResp.StepHash != string(agentStepHash) {
		t.Fatalf("files after the agent step = %+v, want source=session step=%s", filesResp, agentStepHash)
	}

	// 7. Blame: the untouched file's lines stay attributed to the sync step...
	var untouchedBlame blameResponseE2E
	getJSON(t, ts.URL+"/"+repoID+"/api/blame?step="+string(agentStepHash)+"&path=untouched.txt", &untouchedBlame)
	if len(untouchedBlame.Lines) != 2 {
		t.Fatalf("untouched.txt lines = %+v, want 2", untouchedBlame.Lines)
	}
	for _, line := range untouchedBlame.Lines {
		if line.Origin != "sync" || line.StepHash != string(syncHash) {
			t.Errorf("untouched.txt line %+v, want origin=sync step=%s", line, syncHash)
		}
	}

	// ...and the edited file shows the unchanged line still attributed to the
	// sync step, with only the new line attributed to the agent step.
	var editedBlame blameResponseE2E
	getJSON(t, ts.URL+"/"+repoID+"/api/blame?step="+string(agentStepHash)+"&path=edited.txt", &editedBlame)
	if len(editedBlame.Lines) != 2 {
		t.Fatalf("edited.txt lines = %+v, want 2", editedBlame.Lines)
	}
	if editedBlame.Lines[0].Origin != "sync" || editedBlame.Lines[0].StepHash != string(syncHash) {
		t.Errorf("edited.txt unchanged line = %+v, want origin=sync step=%s", editedBlame.Lines[0], syncHash)
	}
	if editedBlame.Lines[1].Origin != "claude_code" || editedBlame.Lines[1].StepHash != string(agentStepHash) {
		t.Errorf("edited.txt new line = %+v, want origin=claude_code step=%s", editedBlame.Lines[1], agentStepHash)
	}

	// 8. The sync ref must never be mistaken for a session.
	var sessionsResp sessionsResponseE2E
	getJSON(t, ts.URL+"/"+repoID+"/api/sessions", &sessionsResp)
	if sessionsResp.TotalSessions != 1 || len(sessionsResp.Sessions) != 1 || sessionsResp.Sessions[0].SessionID != "claude_code--sess1" {
		t.Fatalf("sessions = %+v, want only claude_code--sess1", sessionsResp)
	}
}

// --- helpers ---

type filesResponseE2E struct {
	StepHash   string `json:"step_hash"`
	TotalFiles int    `json:"total_files"`
	Source     string `json:"source"`
}

type blameLineE2E struct {
	Content  string `json:"content"`
	StepHash string `json:"step_hash"`
	Origin   string `json:"origin"`
}

type blameResponseE2E struct {
	Lines []blameLineE2E `json:"lines"`
}

type sessionSummaryE2E struct {
	SessionID string `json:"session_id"`
}

type sessionsResponseE2E struct {
	TotalSessions int                 `json:"total_sessions"`
	Sessions      []sessionSummaryE2E `json:"sessions"`
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// createTestRepo registers repoID with the server, mirroring server_test.go's
// createRepo helper (unexported in package server, so duplicated here for a
// black-box test).
func createTestRepo(t *testing.T, ts *httptest.Server, repoID string) int {
	t.Helper()
	body, err := json.Marshal(map[string]string{"repo_id": repoID})
	if err != nil {
		t.Fatalf("marshal repo_id: %v", err)
	}
	resp, err := http.Post(ts.URL+"/repos", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /repos: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestE2EConnectDeliversWorkspaceBaselineToServer is the regression test for
// the gap closed by this fix: rgt connect's first-run baseline snapshot must
// not just sit in the machine-local server-mode cache — it must reach the
// server, so GET /{repo}/api/files (no params) lists it immediately, with no
// agent ever having run a turn. Before the fix, runBaselineSync wrote the
// sync step locally and stopped there: the server's Files view stayed empty
// after connect, the exact symptom issue #106 is about. It runs at the real
// binary / real server seam (see the doc comment atop e2e_server_test.go)
// because that is the only place "did connect actually deliver it" can be
// asked honestly.
func TestE2EConnectDeliversWorkspaceBaselineToServer(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)

	project := filepath.Join(t.TempDir(), "baseline-project")
	mustMkdirAll(t, project)
	mustMkdirAll(t, filepath.Join(project, ".git"))
	writeTestFile(t, project, "README.md", "hello\n")
	writeTestFile(t, project, "main.go", "package main\n")

	env := hermeticEnv(t, srv)
	out := e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL, "--no-git-hook")
	assertContains(t, out, "Baseline snapshot", "connect output")

	key := repoIDOf(t, project)
	if key == "" {
		t.Fatalf("connect wrote no project identity into .regent/config.toml:\n%s", out)
	}

	var filesResp filesResponseE2E
	getJSON(t, srv.URL+"/"+key+"/api/files", &filesResp)
	if filesResp.Source != "sync" {
		t.Fatalf("files response = %+v, want source=sync (the baseline reached the server)\nconnect output:\n%s", filesResp, out)
	}
	// >=2, not ==2: connect's own scaffolding (the bootstrap skill under
	// .claude/skills/, etc.) is part of the same working tree the baseline
	// snapshots, so the count legitimately exceeds the two files this test
	// wrote. What matters here is that the server has *a* baseline at all.
	if filesResp.TotalFiles < 2 {
		t.Fatalf("files response = %+v, want at least 2 files", filesResp)
	}
}
