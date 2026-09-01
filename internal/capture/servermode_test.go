package capture_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/bonez-io/re_gent/internal/capture"
	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/remote/remotecapture"
	"github.com/bonez-io/re_gent/internal/remotetest"
	"github.com/bonez-io/re_gent/internal/store"
)

const testSessionRef = "sessions/claude_code--sess-1"

type serverModeEnv struct {
	workspace string
	cfg       remote.Config
	srv       *remotetest.Server
}

// newServerModeEnv builds a workspace that deliberately has no .regent/
// directory: server mode must work in a repository that was never initialised.
func newServerModeEnv(t *testing.T) *serverModeEnv {
	t.Helper()

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	srv := remotetest.New()
	t.Cleanup(srv.Close)

	return &serverModeEnv{
		workspace: workspace,
		srv:       srv,
		cfg: remote.Config{
			ServerURL: srv.URL(),
			RepoID:    "test-repo",
			CacheDir:  t.TempDir(),
			Timeout:   2 * time.Second,
		},
	}
}

// runTurn drives one complete agent turn through the recorder, the way the
// Claude Code hooks do: prompt, tool call, then finalize.
func runTurn(t *testing.T, rec *Recorder, turnID, content string) {
	t.Helper()

	meta := SessionMetadata{SessionID: "sess-1", Origin: OriginClaudeCode}

	if err := rec.RecordUserPrompt(UserPrompt{SessionMetadata: meta, TurnID: turnID, Prompt: "do " + turnID}); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rec.CWD, "main.go"), []byte(content), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	if err := rec.RecordToolUse(ToolUse{
		SessionMetadata: meta,
		TurnID:          turnID,
		ToolName:        "Write",
		ToolUseID:       "tool-" + turnID,
		ToolInput:       json.RawMessage(`{"file_path":"main.go"}`),
		ToolResponse:    json.RawMessage(`{"ok":true}`),
	}); err != nil {
		t.Fatalf("RecordToolUse: %v", err)
	}
	if err := rec.RecordAssistantAndFinalize(AssistantResponse{
		SessionMetadata:      meta,
		TurnID:               turnID,
		LastAssistantMessage: "done " + turnID,
	}); err != nil {
		t.Fatalf("RecordAssistantAndFinalize: %v", err)
	}
}

func TestServerModeCapturesWithoutLocalRegentDir(t *testing.T) {
	env := newServerModeEnv(t)

	rec, _, err := remotecapture.Open(env.workspace, env.cfg)
	if err != nil {
		t.Fatalf("OpenServerMode: %v", err)
	}
	defer func() { _ = rec.Close() }()

	runTurn(t, rec, "turn-1", "package main // one\n")

	// The whole point of the cutover: the repository stays clean.
	if _, err := os.Stat(filepath.Join(env.workspace, ".regent")); !os.IsNotExist(err) {
		t.Fatalf(".regent/ must not be created in the working tree (stat err = %v)", err)
	}

	tip := env.srv.Ref(testSessionRef)
	if tip == "" {
		t.Fatal("the server has no session ref; capture never reached the source of truth")
	}

	// The server must hold the step, its tree, and the file content — not just
	// a pointer to bytes that only exist on this machine.
	objects := env.srv.Objects()
	stepBytes, ok := objects[tip]
	if !ok {
		t.Fatalf("server is missing the step object %s", tip)
	}
	var step store.Step
	if err := json.Unmarshal(stepBytes, &step); err != nil {
		t.Fatalf("decode server step: %v", err)
	}
	treeBytes, ok := objects[step.Tree]
	if !ok {
		t.Fatalf("server is missing tree %s", step.Tree)
	}
	var tree store.Tree
	if err := json.Unmarshal(treeBytes, &tree); err != nil {
		t.Fatalf("decode server tree: %v", err)
	}

	var found bool
	for _, entry := range tree.Entries {
		if entry.Path != "main.go" {
			continue
		}
		found = true
		content, ok := objects[entry.Blob]
		if !ok {
			t.Fatalf("server is missing file blob %s", entry.Blob)
		}
		if string(content) != "package main // one\n" {
			t.Fatalf("server stored %q", content)
		}
	}
	if !found {
		t.Fatalf("tree on the server does not contain main.go: %+v", tree.Entries)
	}
}

func TestServerModeSurvivesAnOutageAndConverges(t *testing.T) {
	env := newServerModeEnv(t)

	rec, link, err := remotecapture.Open(env.workspace, env.cfg)
	if err != nil {
		t.Fatalf("OpenServerMode: %v", err)
	}
	defer func() { _ = rec.Close() }()

	// The server is unreachable for the whole first turn. The turn must still
	// complete: capture reports no error to the hook, and nothing is lost.
	env.srv.SetOffline(true)
	runTurn(t, rec, "turn-1", "package main // offline\n")

	if env.srv.Ref(testSessionRef) != "" {
		t.Fatal("the server should have received nothing while offline")
	}
	status, err := link.Spool.Status(rec.Store)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Clean() || status.PendingSteps != 1 {
		t.Fatalf("status = %+v, want exactly one queued step", status)
	}

	// The failure must be visible to the user, not silent.
	logBody := readHookErrorLog(t, rec.Store.Root)
	if !strings.Contains(logBody, "rgt sync") {
		t.Fatalf("hook error log does not point at the recovery command:\n%s", logBody)
	}

	// The server comes back. The next turn delivers both turns' work.
	env.srv.SetOffline(false)
	link.Now = func() time.Time { return time.Now().Add(61 * time.Second) }
	runTurn(t, rec, "turn-2", "package main // online\n")

	localTip, err := rec.Store.ReadRef(testSessionRef)
	if err != nil {
		t.Fatalf("read local ref: %v", err)
	}
	if got := env.srv.Ref(testSessionRef); got != localTip {
		t.Fatalf("server ref = %s, local ref = %s; they must converge", got, localTip)
	}

	status, err = link.Spool.Status(rec.Store)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Clean() {
		t.Fatalf("outbox still has work after recovery: %+v", status)
	}

	// Both steps, including the one captured during the outage, are on the
	// server with their contents.
	objects := env.srv.Objects()
	steps := 0
	for current := localTip; current != ""; {
		if _, ok := objects[current]; !ok {
			t.Fatalf("server is missing step %s", current)
		}
		step, err := rec.Store.ReadStep(current)
		if err != nil {
			t.Fatalf("read step: %v", err)
		}
		if _, ok := objects[step.Tree]; !ok {
			t.Fatalf("server is missing tree %s", step.Tree)
		}
		steps++
		current = step.Parent
	}
	if steps != 2 {
		t.Fatalf("delivered %d steps, want 2", steps)
	}
}

func TestServerModeCooldownStopsHammeringADeadServer(t *testing.T) {
	env := newServerModeEnv(t)

	rec, link, err := remotecapture.Open(env.workspace, env.cfg)
	if err != nil {
		t.Fatalf("OpenServerMode: %v", err)
	}
	defer func() { _ = rec.Close() }()

	now := time.Unix(1_700_000_000, 0)
	link.Now = func() time.Time { return now }

	env.srv.SetOffline(true)
	runTurn(t, rec, "turn-1", "package main // offline\n")

	attempts := env.srv.Requests(http.MethodGet) + env.srv.Requests(http.MethodPost)
	if attempts == 0 {
		t.Fatal("the first sync should have tried to reach the server")
	}

	// Inside the cooldown window nothing is attempted, so an outage costs the
	// agent one timeout per window instead of one per hook invocation.
	link.Sync(rec, "second attempt")
	if got := env.srv.Requests(http.MethodGet) + env.srv.Requests(http.MethodPost); got != attempts {
		t.Fatalf("made %d extra request(s) during the cooldown window", got-attempts)
	}

	// Once the window expires, delivery resumes automatically.
	env.srv.SetOffline(false)
	now = now.Add(31 * time.Second)
	link.Sync(rec, "after cooldown")

	if env.srv.Ref(testSessionRef) == "" {
		t.Fatal("delivery did not resume after the cooldown expired")
	}
}

func TestServerModeDeliversArchivedTranscripts(t *testing.T) {
	env := newServerModeEnv(t)

	rec, link, err := remotecapture.Open(env.workspace, env.cfg)
	if err != nil {
		t.Fatalf("OpenServerMode: %v", err)
	}
	defer func() { _ = rec.Close() }()

	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	body := []byte(`{"role":"assistant","content":"hello"}` + "\n")
	if err := os.WriteFile(transcript, body, 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	if err := rec.ArchiveTranscript("claude_code--sess-1", transcript); err != nil {
		t.Fatalf("ArchiveTranscript: %v", err)
	}

	// A transcript archive hangs off no step, so only the explicit queue can
	// carry it to the server.
	queued, err := link.Spool.PendingObjects()
	if err != nil {
		t.Fatalf("PendingObjects: %v", err)
	}
	if len(queued) != 1 || queued[0] != store.HashBytes(body) {
		t.Fatalf("queued = %v, want the transcript blob", queued)
	}

	link.Sync(rec, "test")
	if _, ok := env.srv.Objects()[store.HashBytes(body)]; !ok {
		t.Fatal("archived transcript never reached the server")
	}
}

func TestServerModeCacheLivesOutsideTheWorkspace(t *testing.T) {
	env := newServerModeEnv(t)

	rec, _, err := remotecapture.Open(env.workspace, env.cfg)
	if err != nil {
		t.Fatalf("OpenServerMode: %v", err)
	}
	defer func() { _ = rec.Close() }()

	if strings.HasPrefix(rec.Store.Root, env.workspace) {
		t.Fatalf("cache %s is inside the workspace; snapshots would capture themselves", rec.Store.Root)
	}
	entries, err := os.ReadDir(env.workspace)
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".regent") {
			t.Fatalf("server mode created %s in the working tree", e.Name())
		}
	}
}

func TestOpenWithoutServerConfigOrLocalStoreIsANoOp(t *testing.T) {
	// Neutralise any ambient configuration: an empty value overrides the file.
	t.Setenv("REGENT_SERVER_URL", "")
	t.Setenv("REGENT_REPO_ID", "")

	rec, ok, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open on an uninitialised directory must not error: %v", err)
	}
	if ok || rec != nil {
		t.Fatal("Open must be a no-op when there is neither server config nor .regent/")
	}
}

func TestRemoteCaptureOpenRejectsBadInput(t *testing.T) {
	valid := remote.Config{ServerURL: "https://a.example", RepoID: "repo", CacheDir: t.TempDir()}

	if _, _, err := remotecapture.Open("", valid); err == nil {
		t.Error("an empty cwd must be rejected")
	}
	bad := valid
	bad.RepoID = "../escape"
	if _, _, err := remotecapture.Open(t.TempDir(), bad); err == nil {
		t.Error("an unsafe repo id must be rejected")
	}
}

func readHookErrorLog(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "log", "hook-error.log"))
	if err != nil {
		t.Fatalf("read hook error log: %v", err)
	}
	return string(data)
}
