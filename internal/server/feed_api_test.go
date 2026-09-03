package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
	"github.com/bonez-io/re_gent/serverauth"
)

// TestAPIFeedPermissionMatchesFiles asserts /api/feed is classified by
// permissionForRequest exactly like the sibling /api/files route: same
// action, same resource kind. The tutorial's contract depends on the feed
// inheriting the same reader-can-read/anonymous-cannot policy as every other
// "/{project}/api/*" route, which permissionForRequest grants generically by
// matching on segs[1] == "api" rather than the specific api sub-path — this
// test pins that behavior for /api/feed specifically.
func TestAPIFeedPermissionMatchesFiles(t *testing.T) {
	srv, _, _ := newTestServer(t)

	feedReq := httptest.NewRequest(http.MethodGet, "/alpha/api/feed?since=1", nil)
	feedPerm := srv.permissionForRequest(feedReq, []string{"alpha", "api", "feed"})

	filesReq := httptest.NewRequest(http.MethodGet, "/alpha/api/files", nil)
	filesPerm := srv.permissionForRequest(filesReq, []string{"alpha", "api", "files"})

	if feedPerm.Action != filesPerm.Action {
		t.Errorf("feed action = %v, want %v (same as /api/files)", feedPerm.Action, filesPerm.Action)
	}
	if feedPerm.Action != serverauth.ActionHistoryRead {
		t.Errorf("feed action = %v, want ActionHistoryRead", feedPerm.Action)
	}
	if feedPerm.Resource.Kind != filesPerm.Resource.Kind {
		t.Errorf("feed resource kind = %q, want %q (same as /api/files)", feedPerm.Resource.Kind, filesPerm.Resource.Kind)
	}
	if feedPerm.Resource.RepositoryID != filesPerm.Resource.RepositoryID {
		t.Errorf("feed repository id = %q, want %q", feedPerm.Resource.RepositoryID, filesPerm.Resource.RepositoryID)
	}
}

// TestAPIFeedNoSinceReturnsCursorImmediately asserts a request with no
// "since" reports the current cursor and an empty steps array right away,
// rather than entering the long-poll loop.
func TestAPIFeedNoSinceReturnsCursorImmediately(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"
	const sessionID = "claude_code--feed-nosince"

	tipTS := time.Now().UnixNano()
	tipHash := putStep(t, ts, repo, store.Step{
		Tree: "deadbeef", SessionID: sessionID, Origin: "claude_code", TimestampNanos: tipTS,
	})
	if code, _ := postRef(t, ts, repo, "sessions/"+sessionID, "", string(tipHash)); code != http.StatusOK {
		t.Fatalf("set session ref: status %d", code)
	}

	start := time.Now()
	status, body := getAPI(t, ts, "/"+repo+"/api/feed", "")
	elapsed := time.Since(start)
	if status != http.StatusOK {
		t.Fatalf("GET /api/feed: status %d, body %s", status, body)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("no-since request took %v, want an immediate reply", elapsed)
	}

	var got feedResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode feed: %v (body %s)", err, body)
	}
	if len(got.Steps) != 0 {
		t.Errorf("steps = %#v, want none for a no-since request", got.Steps)
	}
	if want := strconv.FormatInt(tipTS, 10); got.Cursor != want {
		t.Errorf("cursor = %q, want %q (the tip step's timestamp)", got.Cursor, want)
	}
}

// TestAPIFeedLongPollReturnsEarlyOnNewStep asserts a since-bearing request
// with no steps yet available returns as soon as a step lands, rather than
// waiting out the full timeout. A background goroutine appends a step after
// a short delay while the request is in flight.
func TestAPIFeedLongPollReturnsEarlyOnNewStep(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"
	const sessionID = "claude_code--feed-longpoll"

	rootTS := time.Now().Add(-time.Hour).UnixNano()
	rootHash := putStep(t, ts, repo, store.Step{
		Tree: "deadbeef", SessionID: sessionID, Origin: "claude_code", TimestampNanos: rootTS,
	})
	if code, _ := postRef(t, ts, repo, "sessions/"+sessionID, "", string(rootHash)); code != http.StatusOK {
		t.Fatalf("set session ref: status %d", code)
	}
	cursor := strconv.FormatInt(rootTS, 10)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	var tipHash store.Hash
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(300 * time.Millisecond)

		tipTS := time.Now().UnixNano()
		tipStep := store.Step{
			Parent: rootHash, Tree: "cafebabe", SessionID: sessionID,
			Origin: "claude_code", TimestampNanos: tipTS,
		}
		tipStep.NormalizeCauses()
		data, err := json.Marshal(&tipStep)
		if err != nil {
			errs <- err
			return
		}
		h := hashOf(data)
		if code := putObjectAs(t, ts, repo, string(h), data); code != http.StatusCreated && code != http.StatusOK {
			errs <- fmt.Errorf("PUT step: status %d", code)
			return
		}
		tipHash = h

		body, _ := json.Marshal(map[string]string{"old": string(rootHash), "new": string(h)})
		resp, err := http.Post(fmt.Sprintf("%s/%s/refs/sessions/%s", ts.URL, repo, sessionID), "application/json", bytes.NewReader(body))
		if err != nil {
			errs <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			errs <- fmt.Errorf("advance session ref: status %d", resp.StatusCode)
		}
	}()

	start := time.Now()
	status, respBody := getAPI(t, ts, "/"+repo+"/api/feed?since="+cursor+"&timeout=3", "")
	elapsed := time.Since(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("background step write failed: %v", err)
	}

	if status != http.StatusOK {
		t.Fatalf("GET /api/feed: status %d, body %s", status, respBody)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("long-poll took %v, want an early return once the step landed (well under the 3s timeout)", elapsed)
	}

	var got feedResponse
	if err := json.Unmarshal(respBody, &got); err != nil {
		t.Fatalf("decode feed: %v (body %s)", err, respBody)
	}
	if len(got.Steps) != 1 {
		t.Fatalf("steps = %#v, want exactly the one new step", got.Steps)
	}
	if got.Steps[0].Hash != string(tipHash) {
		t.Errorf("step hash = %q, want %q", got.Steps[0].Hash, tipHash)
	}
	if got.Steps[0].SessionID != sessionID {
		t.Errorf("session_id = %q, want %q", got.Steps[0].SessionID, sessionID)
	}
}

// TestAPIFeedTimeoutReturnsEmptyWithSameCursor asserts that when nothing new
// arrives, a since-bearing request waits out its timeout and returns an
// empty steps array with the cursor unchanged.
func TestAPIFeedTimeoutReturnsEmptyWithSameCursor(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"
	if code := createRepo(t, ts, repo); code != http.StatusCreated {
		t.Fatalf("create repo: status %d", code)
	}

	cursor := strconv.FormatInt(time.Now().UnixNano(), 10)

	start := time.Now()
	status, body := getAPI(t, ts, "/"+repo+"/api/feed?since="+cursor+"&timeout=1", "")
	elapsed := time.Since(start)
	if status != http.StatusOK {
		t.Fatalf("GET /api/feed: status %d, body %s", status, body)
	}
	if elapsed < time.Second {
		t.Fatalf("timeout request returned after %v, want it to wait out the 1s timeout", elapsed)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("timeout request took %v, want close to the requested 1s timeout", elapsed)
	}

	var got feedResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode feed: %v (body %s)", err, body)
	}
	if len(got.Steps) != 0 {
		t.Errorf("steps = %#v, want none on timeout", got.Steps)
	}
	if got.Cursor != cursor {
		t.Errorf("cursor = %q, want unchanged %q", got.Cursor, cursor)
	}
}

// TestAPIFeedFilesAndPromptPopulated builds a two-step session the way
// TestReadAPIRealConversationFilesStepAndBlame does for /api/files and
// /api/blame — a real tree/blob pair plus a conversation blob carrying a user
// prompt — and asserts /api/feed surfaces both the changed file and the
// prompt (truncated) for the tip step.
func TestAPIFeedFilesAndPromptPopulated(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"
	const sessionID = "claude_code--feed-content"
	const filePath = "hello_world.py"

	rootContent := []byte("print('hello')\n")
	_, rootBlob := putObject(t, ts, repo, rootContent)
	rootTree := putTree(t, ts, repo, store.TreeEntry{Path: filePath, Blob: rootBlob, Mode: 0o644})
	rootTS := time.Now().Add(-time.Minute).UnixNano()
	rootHash := putStep(t, ts, repo, store.Step{
		Tree: rootTree, SessionID: sessionID, Origin: "claude_code", TimestampNanos: rootTS,
	})

	longPrompt := "write a failing test for hello_world.py " + string(bytes.Repeat([]byte("x"), 250))
	convData := []byte(fmt.Sprintf(`[{"type":"user","text":%q,"ts":1},{"type":"assistant","text":"on it","ts":2}]`, longPrompt))
	_, convHash := putObject(t, ts, repo, convData)

	tipContent := []byte("print('hello')\nassert True\n")
	_, tipBlob := putObject(t, ts, repo, tipContent)
	tipTree := putTree(t, ts, repo,
		store.TreeEntry{Path: filePath, Blob: tipBlob, Mode: 0o644},
		store.TreeEntry{Path: "test_hello_world.py", Blob: rootBlob, Mode: 0o644},
	)
	tipTS := time.Now().UnixNano()
	tipHash := putStep(t, ts, repo, store.Step{
		Parent: rootHash, Tree: tipTree, Conversation: convHash, SessionID: sessionID,
		Origin: "claude_code", TurnID: "turn-2", TimestampNanos: tipTS,
	})
	if code, _ := postRef(t, ts, repo, "sessions/"+sessionID, "", string(tipHash)); code != http.StatusOK {
		t.Fatalf("set session ref: status %d", code)
	}

	cursor := strconv.FormatInt(rootTS, 10)
	status, body := getAPI(t, ts, "/"+repo+"/api/feed?since="+cursor+"&timeout=3", "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/feed: status %d, body %s", status, body)
	}
	var got feedResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode feed: %v (body %s)", err, body)
	}
	if len(got.Steps) != 1 {
		t.Fatalf("steps = %#v, want exactly the tip step", got.Steps)
	}
	step := got.Steps[0]
	if step.Hash != string(tipHash) {
		t.Errorf("hash = %q, want %q", step.Hash, tipHash)
	}
	if step.Origin != "claude_code" || step.TurnID != "turn-2" {
		t.Errorf("origin/turn_id = %q/%q, want claude_code/turn-2", step.Origin, step.TurnID)
	}
	wantFiles := map[string]bool{filePath: true, "test_hello_world.py": true}
	if len(step.Files) != len(wantFiles) {
		t.Fatalf("files = %#v, want %v", step.Files, wantFiles)
	}
	for _, f := range step.Files {
		if !wantFiles[f] {
			t.Errorf("unexpected file %q in %#v", f, step.Files)
		}
	}
	if len(step.Prompt) != feedPromptChars {
		t.Errorf("prompt length = %d, want truncated to %d", len(step.Prompt), feedPromptChars)
	}
	if step.Prompt != string([]rune(longPrompt)[:feedPromptChars]) {
		t.Errorf("prompt = %q, want the first %d runes of the user prompt", step.Prompt, feedPromptChars)
	}
}

// TestAPIFeedIncludesSyncRefTips asserts a step recorded only under
// refs/sync/* (no refs/sessions/* ref at all) is still surfaced by the feed,
// per the contract's "also include refs/sync/* tips if that ref directory
// exists" requirement.
func TestAPIFeedIncludesSyncRefTips(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"

	since := time.Now().Add(-time.Hour).UnixNano()
	syncTS := time.Now().UnixNano()
	syncHash := putStep(t, ts, repo, store.Step{
		Tree: "deadbeef", SessionID: "sync-worker", Origin: "sync", TimestampNanos: syncTS,
	})
	if code, _ := postRef(t, ts, repo, "sync/workspace", "", string(syncHash)); code != http.StatusOK {
		t.Fatalf("set sync ref: status %d", code)
	}

	cursor := strconv.FormatInt(since, 10)
	status, body := getAPI(t, ts, "/"+repo+"/api/feed?since="+cursor+"&timeout=3", "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/feed: status %d, body %s", status, body)
	}
	var got feedResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode feed: %v (body %s)", err, body)
	}
	if len(got.Steps) != 1 || got.Steps[0].Hash != string(syncHash) {
		t.Fatalf("steps = %#v, want the one sync-origin step", got.Steps)
	}
	if got.Steps[0].Origin != "sync" {
		t.Errorf("origin = %q, want sync", got.Steps[0].Origin)
	}
}

// TestAPIFeedInvalidSinceTreatedAsNoSince asserts a malformed since value is
// treated as absent rather than rejected with 400.
func TestAPIFeedInvalidSinceTreatedAsNoSince(t *testing.T) {
	_, _, ts := newTestServer(t)
	if code := createRepo(t, ts, "alpha"); code != http.StatusCreated {
		t.Fatalf("create repo: status %d", code)
	}
	status, body := getAPI(t, ts, "/alpha/api/feed?since=not-a-number", "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/feed with an invalid since: status %d, body %s", status, body)
	}
	var got feedResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode feed: %v (body %s)", err, body)
	}
	if len(got.Steps) != 0 {
		t.Errorf("steps = %#v, want none (invalid since behaves like no since)", got.Steps)
	}
}
