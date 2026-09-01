package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/store"
)

func TestSessionsCmdJSONFormat(t *testing.T) {
	root := t.TempDir()
	withWorkingDir(t, root)

	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	idx, err := index.Open(s)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}

	sessionID := "claude-20260502-143021"
	agentID := "claude-code"
	firstStep := writeIndexedSessionStep(t, s, idx, sessionID, "", agentID, time.Date(2026, 5, 2, 14, 30, 21, 0, time.UTC))
	writeIndexedSessionStep(t, s, idx, sessionID, firstStep, agentID, time.Date(2026, 5, 2, 14, 31, 21, 0, time.UTC))

	if err := idx.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	cmd := SessionsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--format=json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute sessions command: %v", err)
	}

	var got struct {
		TotalSessions int `json:"total_sessions"`
		Sessions      []struct {
			SessionID    string `json:"session_id"`
			StepCount    int    `json:"step_count"`
			LastActivity string `json:"last_activity"`
			AgentID      string `json:"agent_id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out.String())
	}

	if got.TotalSessions != 1 {
		t.Fatalf("total_sessions = %d, want 1; output: %s", got.TotalSessions, out.String())
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions length = %d, want 1; output: %s", len(got.Sessions), out.String())
	}

	session := got.Sessions[0]
	if session.SessionID != sessionID {
		t.Fatalf("session_id = %q, want %q", session.SessionID, sessionID)
	}
	if session.StepCount != 2 {
		t.Fatalf("step_count = %d, want 2", session.StepCount)
	}
	if session.AgentID != agentID {
		t.Fatalf("agent_id = %q, want %q", session.AgentID, agentID)
	}
	if _, err := time.Parse(time.RFC3339, session.LastActivity); err != nil {
		t.Fatalf("last_activity is not RFC3339: %q", session.LastActivity)
	}
}

// TestSessionsCmdJSONIncludesAuthor covers the field the viewer needs. Without
// an author in the machine-readable output there is nothing for a people row to
// render, however many authors the recorded steps carry.
//
// The two steps carry different authors on purpose: a session belongs to
// whoever started it, not whoever touched it last.
func TestSessionsCmdJSONIncludesAuthor(t *testing.T) {
	root := t.TempDir()
	withWorkingDir(t, root)

	s, idx := newSessionsFixture(t, root)

	const sessionID = "claude-20260502-143021"
	first := writeAuthoredSessionStep(t, s, idx, sessionID, "", store.Author{Name: "Ada Lovelace", Email: "ada@example.com"}, time.Date(2026, 5, 2, 14, 30, 21, 0, time.UTC))
	writeAuthoredSessionStep(t, s, idx, sessionID, first, store.Author{Name: "Grace Hopper", Email: "grace@example.com"}, time.Date(2026, 5, 2, 14, 31, 21, 0, time.UTC))

	if err := idx.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	out := runSessionsCmd(t, "--format=json")

	var got struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Author    *struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"author"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions length = %d, want 1; output: %s", len(got.Sessions), out)
	}
	author := got.Sessions[0].Author
	if author == nil {
		t.Fatalf("session has no author field; the viewer's people row has nothing to render.\noutput: %s", out)
	}
	if author.Name != "Ada Lovelace" {
		t.Errorf("author.name = %q, want the author of the first step %q", author.Name, "Ada Lovelace")
	}
	if author.Email != "ada@example.com" {
		t.Errorf("author.email = %q, want %q", author.Email, "ada@example.com")
	}
}

// TestSessionsCmdJSONOmitsAuthorWhenUnknown keeps author-less history listed.
func TestSessionsCmdJSONOmitsAuthorWhenUnknown(t *testing.T) {
	root := t.TempDir()
	withWorkingDir(t, root)

	s, idx := newSessionsFixture(t, root)
	writeAuthoredSessionStep(t, s, idx, "claude-anon", "", store.Author{}, time.Date(2026, 5, 2, 14, 30, 21, 0, time.UTC))
	if err := idx.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	out := runSessionsCmd(t, "--format=json")

	var got struct {
		TotalSessions int `json:"total_sessions"`
		Sessions      []struct {
			Author *struct{} `json:"author"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if got.TotalSessions != 1 || len(got.Sessions) != 1 {
		t.Fatalf("author-less session was not listed; output: %s", out)
	}
	if got.Sessions[0].Author != nil {
		t.Errorf("author field present for a session with no identifiable author; output: %s", out)
	}
}

// TestSessionsCmdTextShowsAuthor is the human-readable half: reading the list in
// a terminal must answer "who ran this".
func TestSessionsCmdTextShowsAuthor(t *testing.T) {
	root := t.TempDir()
	withWorkingDir(t, root)

	s, idx := newSessionsFixture(t, root)
	writeAuthoredSessionStep(t, s, idx, "claude-text", "", store.Author{Name: "Ada Lovelace", Email: "ada@example.com"}, time.Date(2026, 5, 2, 14, 30, 21, 0, time.UTC))
	if err := idx.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	out := runSessionsCmd(t)

	if !strings.Contains(out, "Ada Lovelace") {
		t.Errorf("session listing does not name who ran the session:\n%s", out)
	}
}

func newSessionsFixture(t *testing.T, root string) (*store.Store, *index.DB) {
	t.Helper()

	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	idx, err := index.Open(s)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	return s, idx
}

func runSessionsCmd(t *testing.T, args ...string) string {
	t.Helper()

	cmd := SessionsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute sessions command: %v", err)
	}
	return out.String()
}

func writeAuthoredSessionStep(t *testing.T, s *store.Store, idx *index.DB, sessionID string, parent store.Hash, author store.Author, timestamp time.Time) store.Hash {
	t.Helper()

	blobHash, err := s.WriteBlob([]byte("content-" + timestamp.String()))
	if err != nil {
		t.Fatalf("write blob: %v", err)
	}
	tree := &store.Tree{Entries: []store.TreeEntry{{Path: "file.txt", Blob: blobHash, Mode: 0o644}}}
	treeHash, err := s.WriteTree(tree)
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}
	step := &store.Step{
		Parent:         parent,
		SessionID:      sessionID,
		Origin:         "claude_code",
		AgentID:        "claude-code",
		Author:         author,
		TimestampNanos: timestamp.UnixNano(),
		Tree:           treeHash,
		Cause:          store.Cause{ToolName: "Write", ToolUseID: "tool_" + timestamp.String()},
	}
	stepHash, err := s.WriteStep(step)
	if err != nil {
		t.Fatalf("write step: %v", err)
	}
	if err := idx.IndexStep(stepHash, step, tree); err != nil {
		t.Fatalf("index step: %v", err)
	}
	return stepHash
}

func TestSessionsCmdHelpDocumentsFormatFlag(t *testing.T) {
	cmd := SessionsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute help: %v", err)
	}

	help := out.String()
	if !strings.Contains(help, "--format") {
		t.Fatalf("help does not document --format flag:\n%s", help)
	}
	if !strings.Contains(help, "text or json") {
		t.Fatalf("help does not document supported formats:\n%s", help)
	}
}

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()

	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Fatalf("restore working dir: %v", err)
		}
	})
}

func writeIndexedSessionStep(t *testing.T, s *store.Store, idx *index.DB, sessionID string, parent store.Hash, agentID string, timestamp time.Time) store.Hash {
	t.Helper()

	blobHash, err := s.WriteBlob([]byte("content"))
	if err != nil {
		t.Fatalf("write blob: %v", err)
	}
	tree := &store.Tree{
		Entries: []store.TreeEntry{
			{Path: "file.txt", Blob: blobHash, Mode: 0o644},
		},
	}
	treeHash, err := s.WriteTree(tree)
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}

	step := &store.Step{
		Parent:         parent,
		SessionID:      sessionID,
		Origin:         "claude_code",
		AgentID:        agentID,
		TimestampNanos: timestamp.UnixNano(),
		Tree:           treeHash,
		Cause: store.Cause{
			ToolName:  "Write",
			ToolUseID: "tool_1",
		},
	}

	stepHash, err := s.WriteStep(step)
	if err != nil {
		t.Fatalf("write step: %v", err)
	}
	if err := idx.IndexStep(stepHash, step, tree); err != nil {
		t.Fatalf("index step: %v", err)
	}

	return stepHash
}
