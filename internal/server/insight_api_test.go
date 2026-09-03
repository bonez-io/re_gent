package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/capture"
	"github.com/bonez-io/re_gent/internal/insight"
	"github.com/bonez-io/re_gent/internal/insight/search"
	"github.com/bonez-io/re_gent/internal/store"
)

// recordSession records one tool-using turn into a fresh local store, the
// way a hook would, and returns the store, the canonical session id, and
// the step at the tip.
func recordSession(t *testing.T, sessionID, prompt, reply string) (*store.Store, string, store.Hash) {
	t.Helper()
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := capture.Open(root)
	if err != nil || !ok {
		t.Fatalf("open recorder: %v", err)
	}
	defer rec.Close()
	meta := capture.SessionMetadata{SessionID: sessionID, Origin: capture.OriginClaudeCode}
	if err := rec.RecordUserPrompt(capture.UserPrompt{SessionMetadata: meta, TurnID: "t1", Prompt: prompt}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "queue.go"), []byte("package q\nfunc Retry() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rec.RecordToolUse(capture.ToolUse{
		SessionMetadata: meta, TurnID: "t1", ToolName: "Write", ToolUseID: "w1",
		ToolInput: json.RawMessage(`{"file_path":"queue.go"}`), ToolResponse: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := rec.RecordAssistantAndFinalize(capture.AssistantResponse{SessionMetadata: meta, TurnID: "t1", LastAssistantMessage: reply}); err != nil {
		t.Fatal(err)
	}
	sessions, err := rec.Index.ListAllSessions()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions: %v %v", sessions, err)
	}
	return rec.Store, sessions[0].ID, sessions[0].HeadStepID
}

// pushAll uploads every object of a local store and points the session ref
// at tip, as `rgt sync` would.
func pushAll(t *testing.T, ts *httptest.Server, repo string, local *store.Store, sessionID string, tip store.Hash) {
	t.Helper()
	if err := local.WalkObjects(func(h store.Hash) error {
		data, err := local.ReadBlob(h)
		if err != nil {
			return err
		}
		if code, _ := putObject(t, ts, repo, data); code != http.StatusCreated && code != http.StatusOK {
			return fmt.Errorf("put %s: %d", h, code)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if code, _ := postRef(t, ts, repo, "sessions/"+sessionID, "", string(tip)); code != http.StatusOK {
		t.Fatalf("post ref: %d", code)
	}
}

// cannedReader is a command provider that ignores its input and prints one
// Appendix A reply naming the turn it will be shown.
func cannedReader(t *testing.T, dir string) string {
	t.Helper()
	script := filepath.Join(dir, "reader.sh")
	reply := `{"work_items":[{"id":null,"starts_at_step":"t1","goal":"Add exponential backoff to Retry","approach":"Edited queue.go","outcome":"Retry backs off","status":"done","entities":[{"type":"symbol","name":"queue.go::Retry","role":"touched","confidence":0.9,"evidence_step_id":"t1"}]}]}`
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat > /dev/null\nprintf '%s' '"+reply+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func insightGet(t *testing.T, ts *httptest.Server, path string, out any) int {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("decode %s: %v\n%s", path, err, body)
		}
	}
	return resp.StatusCode
}

func insightPost(t *testing.T, ts *httptest.Server, path string, in any, out any) int {
	t.Helper()
	body, _ := json.Marshal(in)
	resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s: %v\n%s", path, err, raw)
		}
	}
	return resp.StatusCode
}

func TestInsight_ServerReadsPushedSessions(t *testing.T) {
	dataDir := t.TempDir()
	script := cannedReader(t, dataDir)
	cfg := fmt.Sprintf("[model]\nprovider = \"command\"\ncommand = [%q]\n", script)
	if err := os.WriteFile(filepath.Join(dataDir, InsightConfigFile), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	if code := createRepo(t, ts, "proj"); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create repo: %d", code)
	}

	// Off by default: a push is stored but not read.
	local, sessionID, tip := recordSession(t, "push-1", "Add exponential backoff to Retry for https://github.com/acme/demo/issues/7", "Done, Retry backs off.")
	pushAll(t, ts, "proj", local, sessionID, tip)
	var status insight.Status
	if code := insightGet(t, ts, "/proj/api/insight/status", &status); code != http.StatusOK {
		t.Fatalf("status: %d", code)
	}
	if status.Enabled || status.Active || status.Model.Provider != "command" || !strings.HasSuffix(status.ProvidersFrom, InsightConfigFile) {
		t.Fatalf("status before enable: %#v", status)
	}
	var items []search.WorkItem
	insightGet(t, ts, "/proj/api/work", &items)
	if len(items) != 0 {
		t.Fatalf("nothing should be read while off: %#v", items)
	}

	// Enable: the server mirrors what was pushed; run reads it.
	if code := insightPost(t, ts, "/proj/api/insight/settings", map[string]any{"enabled": true}, &status); code != http.StatusOK {
		t.Fatalf("enable: %d", code)
	}
	if !status.Active || status.Coverage.Sessions != 1 || status.Coverage.Messages == 0 {
		t.Fatalf("status after enable should show the mirrored session: %#v", status)
	}
	if code := insightPost(t, ts, "/proj/api/insight/run", map[string]any{}, &status); code != http.StatusOK {
		t.Fatalf("run: %d", code)
	}
	items = waitForWorkItems(t, ts, "/proj/api/work", 1)
	w := items[0]
	if w.Goal != "Add exponential backoff to Retry" || w.Status != "done" || w.SessionID != sessionID || w.Provider != "command" {
		t.Fatalf("work item: %#v", w)
	}
	if len(w.Files) != 1 || w.Files[0] != "queue.go" || len(w.Steps) != 1 || w.Steps[0] != string(tip) {
		t.Fatalf("files/steps: %v %v", w.Files, w.Steps)
	}
	var kinds []string
	for _, e := range w.Entities {
		kinds = append(kinds, e.Type+":"+e.Name+":"+e.Source)
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "issue:acme/demo#7:deterministic") || !strings.Contains(joined, "symbol:queue.go::Retry:model") {
		t.Fatalf("entities: %v", kinds)
	}

	// The routes the CLI uses in server mode.
	var one search.WorkItem
	if code := insightGet(t, ts, "/proj/api/work/"+w.ID, &one); code != http.StatusOK || one.ID != w.ID {
		t.Fatalf("work/{id}: %d %#v", code, one)
	}
	var res search.Result
	if code := insightGet(t, ts, "/proj/api/search?q=backoff", &res); code != http.StatusOK {
		t.Fatalf("search: %d", code)
	}
	if len(res.Hits) != 1 || res.Hits[0].Item.ID != w.ID || len(res.NotYetRead) != 0 {
		t.Fatalf("search result: %#v", res)
	}
	if code := insightGet(t, ts, "/proj/api/search?entity=acme/demo%237", &res); code != http.StatusOK || len(res.Hits) != 1 {
		t.Fatalf("entity search: %d %#v", code, res)
	}
	if code := insightGet(t, ts, "/proj/api/search", nil); code != http.StatusBadRequest {
		t.Fatalf("empty search should be 400, got %d", code)
	}

	// A second push is read on ingest with no further request.
	local2, session2, tip2 := recordSession(t, "push-2", "Write the README", "Wrote README.md.")
	pushAll(t, ts, "proj", local2, session2, tip2)
	items = waitForWorkItems(t, ts, "/proj/api/work", 2)
	if len(items) != 2 {
		t.Fatalf("second session should be read on push: %d", len(items))
	}

	// Disable, then rebuild queues but does not read.
	if code := insightPost(t, ts, "/proj/api/insight/settings", map[string]any{"enabled": false}, &status); code != http.StatusOK || status.Enabled {
		t.Fatalf("disable: %d %#v", code, status)
	}
	if code := insightPost(t, ts, "/proj/api/insight/rebuild", map[string]any{}, &status); code != http.StatusOK || status.Queue["queued"] != 2 {
		t.Fatalf("rebuild while off: %d %#v", code, status)
	}
	if code := insightPost(t, ts, "/proj/api/insight/run", map[string]any{}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("run while off should be 422, got %d", code)
	}
}

func TestInsight_NoProvidersIsIdleNotAnError(t *testing.T) {
	_, _, ts := newTestServer(t)
	createRepo(t, ts, "proj")
	var status insight.Status
	if code := insightPost(t, ts, "/proj/api/insight/settings", map[string]any{"enabled": true}, &status); code != http.StatusOK {
		t.Fatalf("enable: %d", code)
	}
	if !status.Enabled || status.Active || status.ConfigError != "" || status.Model.Provider != "" {
		t.Fatalf("status: %#v", status)
	}
	if code := insightGet(t, ts, "/proj/api/insight/status", &status); code != http.StatusOK {
		t.Fatalf("status: %d", code)
	}
	// Other /api writes stay refused.
	if code := insightPost(t, ts, "/proj/api/sessions", map[string]any{}, nil); code != http.StatusMethodNotAllowed {
		t.Fatalf("POST sessions should be 405, got %d", code)
	}
}

func waitForWorkItems(t *testing.T, ts *httptest.Server, path string, want int) []search.WorkItem {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var items []search.WorkItem
		insightGet(t, ts, path, &items)
		if len(items) >= want {
			return items
		}
		if time.Now().After(deadline) {
			var status insight.Status
			insightGet(t, ts, strings.TrimSuffix(path, "/work")+"/insight/status", &status)
			t.Fatalf("timed out waiting for %d work items; have %d; status %#v", want, len(items), status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
