package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/regent-vcs/regent/internal/store"
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
