package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
)

// getAPI performs an authenticated GET against the test server and returns the
// status code and body. When token is "" no Authorization header is sent.
func getAPI(t *testing.T, ts *httptest.Server, path, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", path, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// putStep marshals a step, uploads it, and returns its hash.
func putStep(t *testing.T, ts *httptest.Server, repo string, step store.Step) store.Hash {
	t.Helper()
	step.NormalizeCauses()
	data, err := json.Marshal(&step)
	if err != nil {
		t.Fatalf("marshal step: %v", err)
	}
	status, h := putObject(t, ts, repo, data)
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("PUT step: status %d", status)
	}
	return h
}

func putTree(t *testing.T, ts *httptest.Server, repo string, entries ...store.TreeEntry) store.Hash {
	t.Helper()
	data, err := json.Marshal(store.Tree{Entries: entries})
	if err != nil {
		t.Fatalf("marshal tree: %v", err)
	}
	status, h := putObject(t, ts, repo, data)
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("PUT tree: status %d", status)
	}
	return h
}

// TestAPISessionsReconstructsTitleAndMetadata builds a two-step session whose
// earliest step carries a known user prompt and asserts /api/sessions returns
// the session with the right title, agent id, step count, and last activity.
func TestAPISessionsReconstructsTitleAndMetadata(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"
	const sessionID = "claude_code--sess1"
	const prompt = "please add a login endpoint"

	// Earliest step: carries the user prompt in its conversation blob.
	convData := []byte(fmt.Sprintf(`[{"type":"user","text":%q,"ts":1},{"type":"assistant","text":"on it","ts":2}]`, prompt))
	_, convHash := putObject(t, ts, repo, convData)

	rootTS := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC).UnixNano()
	rootStep := store.Step{
		Tree:           "deadbeef",
		Conversation:   convHash,
		SessionID:      sessionID,
		Origin:         "claude_code",
		TimestampNanos: rootTS,
	}
	rootHash := putStep(t, ts, repo, rootStep)

	// Tip step: newer timestamp, no conversation of its own.
	tipTS := time.Date(2026, 8, 2, 11, 30, 0, 0, time.UTC).UnixNano()
	tipStep := store.Step{
		Tree:           "cafebabe",
		Parent:         rootHash,
		SessionID:      sessionID,
		Origin:         "claude_code",
		TimestampNanos: tipTS,
	}
	tipHash := putStep(t, ts, repo, tipStep)

	if code, _ := postRef(t, ts, repo, "sessions/"+sessionID, "", string(tipHash)); code != http.StatusOK {
		t.Fatalf("set session ref: status %d", code)
	}

	status, body := getAPI(t, ts, "/"+repo+"/api/sessions", "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/sessions: status %d, body %s", status, body)
	}

	var got sessionsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode sessions: %v (body %s)", err, body)
	}
	if got.TotalSessions != 1 || len(got.Sessions) != 1 {
		t.Fatalf("total_sessions=%d sessions=%d, want 1/1 (body %s)", got.TotalSessions, len(got.Sessions), body)
	}
	sess := got.Sessions[0]
	if sess.SessionID != sessionID {
		t.Errorf("session_id = %q, want %q", sess.SessionID, sessionID)
	}
	if sess.Title != prompt {
		t.Errorf("title = %q, want %q", sess.Title, prompt)
	}
	if sess.AgentID != "claude_code" {
		t.Errorf("agent_id = %q, want claude_code", sess.AgentID)
	}
	if sess.StepCount != 2 {
		t.Errorf("step_count = %d, want 2", sess.StepCount)
	}
	if want := time.Unix(0, tipTS).UTC().Format(time.RFC3339); sess.LastActivity != want {
		t.Errorf("last_activity = %q, want %q", sess.LastActivity, want)
	}
}

// TestAPISessionsIncludesAuthor asserts /api/sessions surfaces the human author
// (name + email) captured on a session's steps, so the viewer can attribute each
// session in a shared team timeline. The tip step has no author of its own, so
// this also exercises the fall-back to the first non-empty author in the walk.
func TestAPISessionsIncludesAuthor(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"
	const sessionID = "claude_code--authsess"
	const wantName = "Ada Lovelace"
	const wantEmail = "ada@team.dev"

	// Earliest step carries the author (as every captured step normally would).
	rootTS := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC).UnixNano()
	rootStep := store.Step{
		Tree:           "deadbeef",
		SessionID:      sessionID,
		Origin:         "claude_code",
		Author:         store.Author{Name: wantName, Email: wantEmail},
		TimestampNanos: rootTS,
	}
	rootHash := putStep(t, ts, repo, rootStep)

	// Tip step has NO author — the summary must fall back to the root's author.
	tipTS := time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC).UnixNano()
	tipStep := store.Step{
		Tree:           "cafebabe",
		Parent:         rootHash,
		SessionID:      sessionID,
		Origin:         "claude_code",
		TimestampNanos: tipTS,
	}
	tipHash := putStep(t, ts, repo, tipStep)

	if code, _ := postRef(t, ts, repo, "sessions/"+sessionID, "", string(tipHash)); code != http.StatusOK {
		t.Fatalf("set session ref: status %d", code)
	}

	status, body := getAPI(t, ts, "/"+repo+"/api/sessions", "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/sessions: status %d, body %s", status, body)
	}
	var got sessionsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode sessions: %v (body %s)", err, body)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions=%d, want 1 (body %s)", len(got.Sessions), body)
	}
	sess := got.Sessions[0]
	if sess.Author == nil {
		t.Fatalf("author is nil, want name=%q email=%q (body %s)", wantName, wantEmail, body)
	}
	if sess.Author.Name != wantName {
		t.Errorf("author.name = %q, want %q", sess.Author.Name, wantName)
	}
	if sess.Author.Email != wantEmail {
		t.Errorf("author.email = %q, want %q", sess.Author.Email, wantEmail)
	}
}

// TestAPISessionsAttributesToFirstStepAuthor pins which of two identifiable
// authors a shared session belongs to. The existing coverage only has one
// candidate — the tip is author-less — so it passes under either rule and says
// nothing about direction.
//
// A session belongs to whoever started it. Reading newest-first instead would
// re-attribute the session every time a different person touched it, so the
// name a teammate sees would change under them as the session grew.
func TestAPISessionsAttributesToFirstStepAuthor(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"
	const sessionID = "claude_code--sharedsess"

	rootStep := store.Step{
		Tree:           "deadbeef",
		SessionID:      sessionID,
		Origin:         "claude_code",
		Author:         store.Author{Name: "Ada Lovelace", Email: "ada@team.dev"},
		TimestampNanos: time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC).UnixNano(),
	}
	rootHash := putStep(t, ts, repo, rootStep)

	tipStep := store.Step{
		Tree:           "cafebabe",
		Parent:         rootHash,
		SessionID:      sessionID,
		Origin:         "claude_code",
		Author:         store.Author{Name: "Grace Hopper", Email: "grace@team.dev"},
		TimestampNanos: time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC).UnixNano(),
	}
	tipHash := putStep(t, ts, repo, tipStep)

	if code, _ := postRef(t, ts, repo, "sessions/"+sessionID, "", string(tipHash)); code != http.StatusOK {
		t.Fatalf("set session ref: status %d", code)
	}

	status, body := getAPI(t, ts, "/"+repo+"/api/sessions", "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/sessions: status %d, body %s", status, body)
	}
	var got sessionsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode sessions: %v (body %s)", err, body)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions=%d, want 1 (body %s)", len(got.Sessions), body)
	}
	author := got.Sessions[0].Author
	if author == nil {
		t.Fatalf("author is nil (body %s)", body)
	}
	if author.Name != "Ada Lovelace" {
		t.Errorf("author.name = %q, want the author who started the session, %q", author.Name, "Ada Lovelace")
	}
	if author.Email != "ada@team.dev" {
		t.Errorf("author.email = %q, want %q", author.Email, "ada@team.dev")
	}
}

// TestAPISessionsOmitsEmptyAuthor asserts a session whose steps carry no author
// (older data, or a host with no git identity) omits the author field entirely
// rather than emitting an empty object the viewer would have to special-case.
func TestAPISessionsOmitsEmptyAuthor(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"
	const sessionID = "claude_code--noauthor"

	step := store.Step{
		Tree:           "deadbeef",
		SessionID:      sessionID,
		Origin:         "claude_code",
		TimestampNanos: time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC).UnixNano(),
	}
	h := putStep(t, ts, repo, step)
	if code, _ := postRef(t, ts, repo, "sessions/"+sessionID, "", string(h)); code != http.StatusOK {
		t.Fatalf("set session ref: status %d", code)
	}

	status, body := getAPI(t, ts, "/"+repo+"/api/sessions", "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/sessions: status %d, body %s", status, body)
	}
	var got sessionsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode sessions: %v (body %s)", err, body)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions=%d, want 1 (body %s)", len(got.Sessions), body)
	}
	if got.Sessions[0].Author != nil {
		t.Errorf("author = %+v, want nil for an author-less session", got.Sessions[0].Author)
	}
}

// TestAPILogReconstructsStepShape builds a one-step session whose step has a
// Write cause and a conversation blob, then asserts /api/log returns the step
// with the right messages, files, causes, and args.
func TestAPILogReconstructsStepShape(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"
	const sessionID = "claude_code--logsess"
	const prompt = "write the config file"
	const filePath = "/repo/config.yaml"

	argsData := []byte(fmt.Sprintf(`{"file_path":%q,"content":"port: 8080"}`, filePath))
	_, argsHash := putObject(t, ts, repo, argsData)
	resultData := []byte(`{"ok":true,"bytes":10}`)
	_, resultHash := putObject(t, ts, repo, resultData)
	convData := []byte(fmt.Sprintf(`[{"type":"user","text":%q,"ts":1},{"type":"reasoning","text":"thinking","ts":2},{"type":"tool_call","tool_name":"Write","tool_use_id":"tu-1","tool_input":%q,"ts":3}]`, prompt, argsHash))
	_, convHash := putObject(t, ts, repo, convData)

	stepTS := time.Date(2026, 8, 2, 9, 15, 0, 0, time.UTC).UnixNano()
	step := store.Step{
		Tree:         "feedface",
		Conversation: convHash,
		Causes: []store.Cause{{
			ToolUseID:  "tu-1",
			ToolName:   "Write",
			ArgsBlob:   argsHash,
			ResultBlob: resultHash,
		}},
		SessionID:      sessionID,
		Origin:         "claude_code",
		TimestampNanos: stepTS,
	}
	stepHash := putStep(t, ts, repo, step)

	if code, _ := postRef(t, ts, repo, "sessions/"+sessionID, "", string(stepHash)); code != http.StatusOK {
		t.Fatalf("set session ref: status %d", code)
	}

	status, body := getAPI(t, ts, "/"+repo+"/api/log?session="+sessionID, "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/log: status %d, body %s", status, body)
	}

	var got logResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode log: %v (body %s)", err, body)
	}
	if got.SessionID != sessionID {
		t.Errorf("session_id = %q, want %q", got.SessionID, sessionID)
	}
	if len(got.Steps) != 1 {
		t.Fatalf("steps = %d, want 1 (body %s)", len(got.Steps), body)
	}
	s0 := got.Steps[0]

	if s0.Hash != string(stepHash) {
		t.Errorf("hash = %q, want %q", s0.Hash, stepHash)
	}
	if s0.Parent != "" {
		t.Errorf("parent = %q, want empty", s0.Parent)
	}
	if s0.Tool != "Write" || s0.ToolUseID != "tu-1" {
		t.Errorf("tool/tool_use_id = %q/%q, want Write/tu-1", s0.Tool, s0.ToolUseID)
	}
	if want := time.Unix(0, stepTS).UTC().Format(time.RFC3339); s0.Timestamp != want {
		t.Errorf("timestamp = %q, want %q", s0.Timestamp, want)
	}

	// messages: first is the user prompt.
	if len(s0.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(s0.Messages))
	}
	if s0.Messages[0].Type != "user" || s0.Messages[0].Message.Role != "user" {
		t.Errorf("messages[0] type/role = %q/%q, want user/user", s0.Messages[0].Type, s0.Messages[0].Message.Role)
	}
	if s0.Messages[0].Message.Content != prompt {
		t.Errorf("messages[0].message.content = %q, want %q", s0.Messages[0].Message.Content, prompt)
	}

	// files: the cause's file_path, deduped.
	if len(s0.Files) != 1 || s0.Files[0] != filePath {
		t.Errorf("files = %v, want [%q]", s0.Files, filePath)
	}

	// causes: parsed args carry the file_path.
	if len(s0.Causes) != 1 {
		t.Fatalf("causes = %d, want 1", len(s0.Causes))
	}
	argsMap, ok := s0.Causes[0].Args.(map[string]interface{})
	if !ok {
		t.Fatalf("causes[0].args is %T, want object", s0.Causes[0].Args)
	}
	if argsMap["file_path"] != filePath {
		t.Errorf("causes[0].args.file_path = %v, want %q", argsMap["file_path"], filePath)
	}
	resMap, ok := s0.Causes[0].Result.(map[string]interface{})
	if !ok || resMap["ok"] != true {
		t.Errorf("causes[0].result = %v, want object with ok=true", s0.Causes[0].Result)
	}
}

// TestAPILogUnknownSession returns a JSON error for a session that has no ref.
func TestAPILogUnknownSession(t *testing.T) {
	_, _, ts := newTestServer(t)
	createRepo(t, ts, "alpha")

	status, body := getAPI(t, ts, "/alpha/api/log?session=claude_code--ghost", "")
	if status != http.StatusNotFound {
		t.Fatalf("status %d, want 404 (body %s)", status, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode error body: %v (body %s)", err, body)
	}
	if got["error"] != "no such session" {
		t.Errorf("error = %q, want \"no such session\"", got["error"])
	}
}

// TestAPIUnknownRepoIs404 verifies a read never creates a repo.
func TestAPIUnknownRepoIs404(t *testing.T) {
	_, _, ts := newTestServer(t)
	if status, _ := getAPI(t, ts, "/ghost/api/sessions", ""); status != http.StatusNotFound {
		t.Fatalf("GET /ghost/api/sessions: status %d, want 404", status)
	}
}

func TestAPISessionsNewestActivityFirst(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"
	newestSecond := time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC).UnixNano()

	for _, fixture := range []struct {
		id string
		ts int64
	}{
		{id: "codex--older", ts: time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC).UnixNano()},
		{id: "z-subsecond-newest", ts: newestSecond + 200},
		{id: "b-tie", ts: newestSecond + 100},
		{id: "a-tie", ts: newestSecond + 100},
	} {
		h := putStep(t, ts, repo, store.Step{
			Tree: "tree", SessionID: fixture.id, Origin: "test", TimestampNanos: fixture.ts,
		})
		if code, _ := postRef(t, ts, repo, "sessions/"+fixture.id, "", string(h)); code != http.StatusOK {
			t.Fatalf("set %s ref: status %d", fixture.id, code)
		}
	}

	status, body := getAPI(t, ts, "/alpha/api/sessions", "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var got sessionsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Sessions) != 4 ||
		got.Sessions[0].SessionID != "z-subsecond-newest" ||
		got.Sessions[1].SessionID != "a-tie" ||
		got.Sessions[2].SessionID != "b-tie" ||
		got.Sessions[3].SessionID != "codex--older" {
		t.Fatalf("sessions order = %#v, want newest activity then session-id tie-break", got.Sessions)
	}
}

func TestAPIStepRequiresFullHash(t *testing.T) {
	_, _, ts := newTestServer(t)
	createRepo(t, ts, "alpha")
	for _, endpoint := range []string{
		"/alpha/api/steps/deadbeef",
		"/alpha/api/files?step=deadbeef",
		"/alpha/api/blame?step=deadbeef&path=README.md",
	} {
		status, body := getAPI(t, ts, endpoint, "")
		if status != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400 (body %s)", endpoint, status, body)
		}
	}
}

func TestReadAPIRealConversationFilesStepAndBlame(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "girlfriend-assistant"
	const sessionID = "codex--mvp"
	const filePath = "internal/reminders.go"
	const capturedHostPath = "/Users/shay/Projects/girlfriend-assistant/internal/reminders.go"

	rootContent := []byte("old reminder\nshared line\n")
	_, rootBlob := putObject(t, ts, repo, rootContent)
	rootTree := putTree(t, ts, repo, store.TreeEntry{Path: filePath, Blob: rootBlob, Mode: 0o644})
	rootConvData := []byte(`[{"type":"user","text":"stabilize reminders","ts":1785661200000000000},{"type":"assistant","text":"I will inspect the scheduler.","ts":1785661201000000000}]`)
	_, rootConv := putObject(t, ts, repo, rootConvData)
	rootTS := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC).UnixNano()
	rootHash := putStep(t, ts, repo, store.Step{
		Tree: rootTree, Conversation: rootConv, SessionID: sessionID, Origin: "codex",
		AgentID: "codex-agent", TurnID: "turn-1", TimestampNanos: rootTS,
		Author: store.Author{Name: "Shay", Email: "shay@example.com"},
	})

	tipContent := []byte("new reminder\nshared line\n")
	_, tipBlob := putObject(t, ts, repo, tipContent)
	tipTree := putTree(t, ts, repo, store.TreeEntry{Path: filePath, Blob: tipBlob, Mode: 0o644})
	argsData := []byte(`{"file_path":"` + capturedHostPath + `","old_string":"old reminder","new_string":"new reminder"}`)
	_, argsHash := putObject(t, ts, repo, argsData)
	resultData := []byte(`{"ok":true}`)
	_, resultHash := putObject(t, ts, repo, resultData)
	tipConvData := []byte(fmt.Sprintf(`[
		{"type":"reasoning","text":"The boundary is wrong.","ts":1785664800000000000},
		{"type":"tool_call","tool_name":"Edit","tool_use_id":"edit-1","tool_input":%q,"ts":1785664801000000000},
		{"type":"tool_result","tool_name":"Edit","tool_use_id":"edit-1","tool_output":%q,"ts":1785664802000000000},
		{"type":"assistant","text":"The reminder boundary is fixed.","ts":1785664803000000000}
	]`, argsHash, resultHash))
	_, tipConv := putObject(t, ts, repo, tipConvData)
	tipTS := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC).UnixNano()
	tipHash := putStep(t, ts, repo, store.Step{
		Parent: rootHash, Tree: tipTree, Conversation: tipConv, SessionID: sessionID,
		Origin: "codex", AgentID: "codex-agent", TurnID: "turn-2", TimestampNanos: tipTS,
		Causes: []store.Cause{{ToolUseID: "edit-1", ToolName: "Edit", ArgsBlob: argsHash, ResultBlob: resultHash}},
		Usage:  &store.Usage{InputTokens: 100, OutputTokens: 20, APICalls: 1},
	})
	if code, _ := postRef(t, ts, repo, "sessions/"+sessionID, "", string(tipHash)); code != http.StatusOK {
		t.Fatalf("set session ref: status %d", code)
	}

	t.Run("status", func(t *testing.T) {
		status, body := getAPI(t, ts, "/"+repo+"/api/status", "")
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, body)
		}
		var got repositoryStatus
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Status != "ok" || got.Service.Name != "re_gent" || got.Repository.ID != repo {
			t.Fatalf("unexpected status: %#v", got)
		}
		if got.Repository.SessionCount != 1 || got.Repository.RefCount != 1 || got.Repository.ObjectCount == 0 {
			t.Fatalf("unexpected counts: %#v", got.Repository)
		}
		if got.Repository.LatestStep != string(tipHash) {
			t.Errorf("latest_step = %s, want %s", got.Repository.LatestStep, tipHash)
		}
	})

	t.Run("transcript is oldest first and resolves portable tools", func(t *testing.T) {
		status, body := getAPI(t, ts, "/"+repo+"/api/transcript?session="+sessionID, "")
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, body)
		}
		var got transcriptResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Session.HeadStep != string(tipHash) || got.Session.StartedAt == "" {
			t.Fatalf("unexpected session: %#v", got.Session)
		}
		if len(got.Steps) != 2 || got.Steps[0].Hash != string(rootHash) || got.Steps[1].Hash != string(tipHash) {
			t.Fatalf("step order = %#v, want root then tip", got.Steps)
		}
		tip := got.Steps[1]
		if tip.Tree != string(tipTree) || tip.Usage == nil || tip.Usage.InputTokens != 100 {
			t.Fatalf("step metadata missing: %#v", tip)
		}
		if len(tip.Events) != 4 || tip.Events[1].Type != "tool_call" || tip.Events[2].Type != "tool_result" {
			t.Fatalf("events = %#v", tip.Events)
		}
		input, ok := tip.Events[1].Input.(map[string]interface{})
		if !ok || input["file_path"] != capturedHostPath {
			t.Fatalf("resolved tool input = %#v", tip.Events[1].Input)
		}
	})

	t.Run("step detail", func(t *testing.T) {
		status, body := getAPI(t, ts, "/"+repo+"/api/steps/"+string(tipHash), "")
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, body)
		}
		var got logStepJSON
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Hash != string(tipHash) || len(got.Causes) != 1 || len(got.Files) != 1 || got.Files[0] != filePath {
			t.Fatalf("unexpected step: %#v", got)
		}
	})

	t.Run("files at session tip", func(t *testing.T) {
		status, body := getAPI(t, ts, "/"+repo+"/api/files?session="+sessionID, "")
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, body)
		}
		var got filesResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.StepHash != string(tipHash) || got.TotalFiles != 1 || got.Files[0].BlobHash != string(tipBlob) || got.Files[0].Size != len(tipContent) {
			t.Fatalf("unexpected files: %#v", got)
		}
	})

	t.Run("blame is derived from canonical history", func(t *testing.T) {
		status, body := getAPI(t, ts, "/"+repo+"/api/blame?session="+sessionID+"&path="+filePath, "")
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, body)
		}
		var got blameResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Lines) != 2 {
			t.Fatalf("lines = %#v", got.Lines)
		}
		if got.Lines[0].Content != "new reminder" || got.Lines[0].StepHash != string(tipHash) {
			t.Errorf("changed line = %#v, want tip attribution", got.Lines[0])
		}
		if got.Lines[1].Content != "shared line" || got.Lines[1].StepHash != string(rootHash) {
			t.Errorf("unchanged line = %#v, want root attribution", got.Lines[1])
		}
	})
}

func TestAPITranscriptSynthesizesLegacyToolEvents(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"
	const sessionID = "claude--legacy"
	_, argsHash := putObject(t, ts, repo, []byte(`{"path":"README.md"}`))
	_, resultHash := putObject(t, ts, repo, []byte(`"ok"`))
	_, convHash := putObject(t, ts, repo, []byte(`[{"type":"user","text":"read it"}]`))
	stepHash := putStep(t, ts, repo, store.Step{
		Tree: "legacy-tree", Conversation: convHash, SessionID: sessionID, Origin: "claude_code",
		TimestampNanos: time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC).UnixNano(),
		Causes:         []store.Cause{{ToolUseID: "read-1", ToolName: "Read", ArgsBlob: argsHash, ResultBlob: resultHash}},
	})
	if code, _ := postRef(t, ts, repo, "sessions/"+sessionID, "", string(stepHash)); code != http.StatusOK {
		t.Fatalf("set session ref: status %d", code)
	}
	status, body := getAPI(t, ts, "/"+repo+"/api/transcript?session="+sessionID, "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var got transcriptResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	events := got.Steps[0].Events
	if len(events) != 3 || events[1].Type != "tool_call" || events[2].Type != "tool_result" {
		t.Fatalf("legacy events = %#v, want user + synthesized call/result", events)
	}
}

// TestAPIFilesNoParamsFallsBackToLatestTree covers the Files view's default
// load: before any step or session is named, /api/files should resolve the
// most recently written tree across every session and workspace-sync ref
// instead of 400ing, and report where it came from.
func TestAPIFilesNoParamsFallsBackToLatestTree(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"

	t.Run("empty repo reports no tree yet", func(t *testing.T) {
		createRepo(t, ts, repo)
		status, body := getAPI(t, ts, "/"+repo+"/api/files", "")
		if status != http.StatusNotFound {
			t.Fatalf("status %d, want 404: %s", status, body)
		}
		var got map[string]string
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got["error"] != "no tree yet" {
			t.Fatalf("error = %q, want %q", got["error"], "no tree yet")
		}
	})

	const repo2 = "beta"
	createRepo(t, ts, repo2)

	syncTree := putTree(t, ts, repo2, store.TreeEntry{Path: "a.txt", Blob: mustPutBlob(t, ts, repo2, "a\n")})
	syncTS := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC).UnixNano()
	syncHash := putStep(t, ts, repo2, store.Step{
		Tree: syncTree, SessionID: "sync:workspace", Origin: "sync", TimestampNanos: syncTS,
	})
	if code, _ := postRef(t, ts, repo2, "sync/workspace", "", string(syncHash)); code != http.StatusOK {
		t.Fatalf("set sync ref: status %d", code)
	}

	t.Run("falls back to the sync baseline when there is no session yet", func(t *testing.T) {
		status, body := getAPI(t, ts, "/"+repo2+"/api/files", "")
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, body)
		}
		var got filesResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Source != "sync" || got.StepHash != string(syncHash) || got.TotalFiles != 1 {
			t.Fatalf("unexpected response: %#v", got)
		}
	})

	sessionTree := putTree(t, ts, repo2, store.TreeEntry{Path: "b.txt", Blob: mustPutBlob(t, ts, repo2, "b\n")})
	sessionTS := syncTS + int64(time.Hour)
	sessionHash := putStep(t, ts, repo2, store.Step{
		Tree: sessionTree, SessionID: "claude_code--sess1", Origin: "claude_code", TimestampNanos: sessionTS,
	})
	if code, _ := postRef(t, ts, repo2, "sessions/claude_code--sess1", "", string(sessionHash)); code != http.StatusOK {
		t.Fatalf("set session ref: status %d", code)
	}

	t.Run("a later session step wins over the sync baseline", func(t *testing.T) {
		status, body := getAPI(t, ts, "/"+repo2+"/api/files", "")
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, body)
		}
		var got filesResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Source != "session" || got.StepHash != string(sessionHash) {
			t.Fatalf("unexpected response: %#v", got)
		}
	})

	t.Run("explicit step and session params are unaffected", func(t *testing.T) {
		status, body := getAPI(t, ts, "/"+repo2+"/api/files?step="+string(syncHash), "")
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, body)
		}
		var got filesResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Source != "sync" || got.StepHash != string(syncHash) {
			t.Fatalf("unexpected response for explicit ?step=: %#v", got)
		}
	})
}

// mustPutBlob is a small convenience wrapper over putObject for tests that
// only care about the resulting hash.
func mustPutBlob(t *testing.T, ts *httptest.Server, repo, content string) store.Hash {
	t.Helper()
	_, h := putObject(t, ts, repo, []byte(content))
	return h
}

// TestAPIBlameForSyncStepReportsSyncOrigin covers the other half of the
// baseline feature: a line whose provenance is a workspace-sync step must
// read as origin "sync" through the same /api/blame endpoint agent-step
// blame already uses, with no server-side special-casing required.
func TestAPIBlameForSyncStepReportsSyncOrigin(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"

	content := []byte("baseline line\n")
	_, blob := putObject(t, ts, repo, content)
	tree := putTree(t, ts, repo, store.TreeEntry{Path: "README.md", Blob: blob, Mode: 0o644})
	syncHash := putStep(t, ts, repo, store.Step{
		Tree: tree, SessionID: "sync:workspace", Origin: "sync",
		TimestampNanos: time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC).UnixNano(),
	})
	if code, _ := postRef(t, ts, repo, "sync/workspace", "", string(syncHash)); code != http.StatusOK {
		t.Fatalf("set sync ref: status %d", code)
	}

	status, body := getAPI(t, ts, "/"+repo+"/api/blame?step="+string(syncHash)+"&path=README.md", "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var got blameResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 1 {
		t.Fatalf("lines = %#v", got.Lines)
	}
	if got.Lines[0].Origin != "sync" || got.Lines[0].StepHash != string(syncHash) {
		t.Fatalf("unexpected blame line: %#v, want origin sync attributed to %s", got.Lines[0], syncHash)
	}
}

// TestAPISessionsIgnoresSyncRef locks in that the workspace-sync ref never
// shows up as a session: /api/sessions enumerates refs/sessions only, and a
// sync/workspace ref sitting alongside it must not appear.
func TestAPISessionsIgnoresSyncRef(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"
	const sessionID = "claude_code--sess1"

	sessionTree := putTree(t, ts, repo, store.TreeEntry{Path: "a.txt", Blob: mustPutBlob(t, ts, repo, "a\n")})
	sessionHash := putStep(t, ts, repo, store.Step{
		Tree: sessionTree, SessionID: sessionID, Origin: "claude_code",
		TimestampNanos: time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC).UnixNano(),
	})
	if code, _ := postRef(t, ts, repo, "sessions/"+sessionID, "", string(sessionHash)); code != http.StatusOK {
		t.Fatalf("set session ref: status %d", code)
	}

	syncTree := putTree(t, ts, repo, store.TreeEntry{Path: "b.txt", Blob: mustPutBlob(t, ts, repo, "b\n")})
	syncHash := putStep(t, ts, repo, store.Step{
		Tree: syncTree, SessionID: "sync:workspace", Origin: "sync",
		TimestampNanos: time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC).UnixNano(),
	})
	if code, _ := postRef(t, ts, repo, "sync/workspace", "", string(syncHash)); code != http.StatusOK {
		t.Fatalf("set sync ref: status %d", code)
	}

	status, body := getAPI(t, ts, "/"+repo+"/api/sessions", "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var got sessionsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.TotalSessions != 1 || len(got.Sessions) != 1 || got.Sessions[0].SessionID != sessionID {
		t.Fatalf("sessions = %#v, want only %s", got.Sessions, sessionID)
	}
}
