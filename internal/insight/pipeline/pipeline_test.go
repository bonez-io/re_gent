package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/capture"
	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/insight"
	"github.com/bonez-io/re_gent/internal/insight/provider"
	"github.com/bonez-io/re_gent/internal/store"
)

// fakeReader answers with whatever the test hands it and remembers what it
// was shown.
type fakeReader struct {
	requests []Request
	replies  []string
	calls    int
}

func (f *fakeReader) Read(_ context.Context, _ string, request []byte) ([]byte, error) {
	var req Request
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	f.requests = append(f.requests, req)
	reply := f.replies[min(f.calls, len(f.replies)-1)]
	f.calls++
	return []byte(reply), nil
}

type fakeEmbedder struct{ calls int }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, float32(len(texts[i]) % 7), 0}
	}
	return out, nil
}

type fixture struct {
	root     string
	recorder *capture.Recorder
	meta     capture.SessionMetadata
	session  string
	proc     *Processor
	reader   *fakeReader
	embedder *fakeEmbedder
	logs     []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := capture.Open(root)
	if err != nil || !ok {
		t.Fatalf("open recorder: ok=%v err=%v", ok, err)
	}
	t.Cleanup(func() { _ = rec.Close() })

	settings, err := insight.Resolve(store.InsightConfig{Enabled: true},
		config.InsightUserConfig{Model: config.InsightModelConfig{Provider: insight.ProviderCommand, Command: []string{"true"}}})
	if err != nil {
		t.Fatal(err)
	}
	scrubber, err := NewScrubber([]string{"ACME Corp"})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{root: root, recorder: rec, reader: &fakeReader{}, embedder: &fakeEmbedder{}}
	f.meta = capture.SessionMetadata{SessionID: "sess-1", Origin: capture.OriginClaudeCode}
	f.session = "sess-1"
	f.proc = &Processor{
		Store: rec.Store, Index: rec.Index, Settings: settings,
		Reader: f.reader, ReadInfo: provider.Info{Provider: "fake", Model: "fake-1"},
		Embedder: f.embedder, EmbedInfo: provider.Info{Provider: "fake-embed", Model: "e1"},
		Scrubber: scrubber,
		Log:      func(l string) { f.logs = append(f.logs, l) },
	}
	return f
}

// turn records one full turn: prompt, optional file write through a tool,
// optional shell tool call with output, and the assistant's final message.
func (f *fixture) turn(t *testing.T, id, prompt, assistant string, files map[string]string, shell ...string) {
	t.Helper()
	if err := f.recorder.RecordUserPrompt(capture.UserPrompt{SessionMetadata: f.meta, TurnID: id, Prompt: prompt}); err != nil {
		t.Fatal(err)
	}
	n := 0
	for path, content := range files {
		n++
		if err := os.WriteFile(filepath.Join(f.root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := f.recorder.RecordToolUse(capture.ToolUse{
			SessionMetadata: f.meta, TurnID: id, ToolName: "Write", ToolUseID: fmt.Sprintf("%s-write-%d", id, n),
			ToolInput: json.RawMessage(fmt.Sprintf(`{"file_path":%q}`, path)), ToolResponse: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(shell) == 2 {
		if err := f.recorder.RecordToolUse(capture.ToolUse{
			SessionMetadata: f.meta, TurnID: id, ToolName: "Bash", ToolUseID: id + "-bash",
			ToolInput:    json.RawMessage(fmt.Sprintf(`{"command":%q}`, shell[0])),
			ToolResponse: json.RawMessage(fmt.Sprintf(`{"stdout":%q}`, shell[1])),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.recorder.RecordAssistantAndFinalize(capture.AssistantResponse{SessionMetadata: f.meta, TurnID: id, LastAssistantMessage: assistant}); err != nil {
		t.Fatal(err)
	}
	// Capture canonicalizes session ids per origin; read back the stored one.
	sessions, err := f.recorder.Index.ListAllSessions()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions: %d err=%v", len(sessions), err)
	}
	f.session = sessions[0].ID
}

func (f *fixture) process(t *testing.T, kind string) {
	t.Helper()
	if err := f.proc.Process(context.Background(), index.InsightJob{Kind: kind, SessionID: f.session}); err != nil {
		t.Fatalf("process: %v\nlogs: %s", err, strings.Join(f.logs, "\n"))
	}
}

func reply(items ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"work_items": items})
	return string(b)
}

func TestProcess_FirstTurnBecomesWorkItem(t *testing.T) {
	f := newFixture(t)
	f.turn(t, "t1", "Fix the flaky retry in queue.go, see https://github.com/bonez-io/re_gent/pull/95 and ACME Corp's ticket",
		"Done: retries now back off.", map[string]string{"queue.go": "package q\nfunc retry() {}\n"},
		"git commit -m fix", "[main abc1234] fix\n 1 file changed")

	f.reader.replies = []string{reply(map[string]any{
		"id": nil, "starts_at_step": "t1",
		"goal": "Fix flaky retry", "approach": "Added backoff", "outcome": "Retries back off", "status": "done",
		"entities": []map[string]any{
			{"type": "Symbol", "name": "queue.go::retry", "role": "touched", "confidence": 0.9, "evidence_step_id": "t1"},
			{"type": "decision", "name": "exponential backoff", "role": "produced", "confidence": 0.8, "evidence_step_id": "nope"},
			{"type": "ticket", "name": "", "evidence_step_id": "t1"},
		},
	})}
	f.process(t, index.InsightJobKindTurn)

	// The request the model saw.
	if len(f.reader.requests) != 1 {
		t.Fatalf("expected one read, got %d", len(f.reader.requests))
	}
	req := f.reader.requests[0]
	if req.OpenWorkItem != nil || len(req.Turns) != 1 {
		t.Fatalf("request: open=%v turns=%d", req.OpenWorkItem, len(req.Turns))
	}
	turn := req.Turns[0]
	if turn.Turn != "t1" || turn.Step == "" || !strings.Contains(turn.User, "flaky") || turn.Assistant != "Done: retries now back off." {
		t.Fatalf("turn view: %#v", turn)
	}
	if len(turn.Files) != 1 || turn.Files[0] != "queue.go" || len(turn.Hunks) != 1 || !strings.Contains(turn.Hunks[0].Diff, "+func retry()") {
		t.Fatalf("files/hunks: %#v %#v", turn.Files, turn.Hunks)
	}
	if !contains(turn.Tools, "Write") || !contains(turn.Tools, "Bash") {
		t.Fatalf("tools: %v", turn.Tools)
	}
	if strings.Contains(turn.User, "ACME Corp") || !strings.Contains(turn.User, "[REDACTED:pattern]") {
		t.Fatalf("scrub pattern not applied: %q", turn.User)
	}
	var types []string
	for _, e := range req.DeterministicEntities {
		types = append(types, e.Type+":"+e.Name)
	}
	if !contains(types, "pull_request:bonez-io/re_gent#95") || !contains(types, "commit:abc1234") {
		t.Fatalf("deterministic entities: %v", types)
	}

	// What was stored.
	items, err := f.proc.Index.ListWorkItems(index.WorkItemFilter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	w := items[0]
	if w.Goal != "Fix flaky retry" || w.Status != "done" || w.EndTS.IsZero() || w.ModelProvider != "fake" || w.PromptVersion != PromptVersion {
		t.Fatalf("item: %#v", w)
	}
	files, _ := f.proc.Index.WorkItemFiles(w.ID)
	if len(files) != 1 || files[0] != "queue.go" {
		t.Fatalf("files: %v", files)
	}
	steps, _ := f.proc.Index.WorkItemSteps(w.ID)
	if len(steps) != 1 || steps[0] != turn.Step {
		t.Fatalf("steps: %v", steps)
	}
	links, _ := f.proc.Index.WorkItemLinks(w.ID)
	got := map[string]index.EntityLink{}
	for _, l := range links {
		got[l.Type+":"+l.Name] = l
	}
	if l, ok := got["symbol:queue.go::retry"]; !ok || l.Source != "model" || l.Role != "touched" || l.EvidenceStepID != "t1" {
		t.Fatalf("model link: %#v (all: %v)", l, keys(got))
	}
	if l, ok := got["pull_request:bonez-io/re_gent#95"]; !ok || l.Source != "deterministic" || l.Ref != "https://github.com/bonez-io/re_gent/pull/95" {
		t.Fatalf("deterministic link: %#v", l)
	}
	if _, ok := got["decision:exponential backoff"]; ok {
		t.Fatal("entity with unknown evidence must be dropped")
	}
	if !strings.Contains(strings.Join(f.logs, "\n"), "cites unknown evidence") {
		t.Fatalf("drop should be logged: %v", f.logs)
	}
	if f.embedder.calls != 1 {
		t.Fatalf("embedder calls: %d", f.embedder.calls)
	}
	vecs, _ := f.proc.Index.Embeddings("work_item", "fake-embed", "e1")
	if len(vecs) != 1 || vecs[0].OwnerID != w.ID {
		t.Fatalf("embeddings: %#v", vecs)
	}
	if cur, _ := f.proc.Index.InsightCursor(f.session); cur < 0 {
		t.Fatal("cursor should advance")
	}

	// Nothing new: no read.
	f.process(t, index.InsightJobKindTurn)
	if f.reader.calls != 1 {
		t.Fatalf("no new turns must not call the model; calls=%d", f.reader.calls)
	}
}

func TestProcess_ExtendsOpenItemThenStartsNew(t *testing.T) {
	f := newFixture(t)
	f.turn(t, "t1", "Start the search feature", "Sketched the schema.", map[string]string{"a.go": "a\n"})
	f.reader.replies = []string{reply(map[string]any{
		"id": nil, "starts_at_step": "t1", "goal": "Build search", "approach": "Schema first", "outcome": "Schema sketched", "status": "wip",
		"entities": []map[string]any{{"type": "concept", "name": "search", "role": "goal", "confidence": 0.9, "evidence_step_id": "t1"}},
	})}
	f.process(t, index.InsightJobKindTurn)

	open, ok, _ := f.proc.Index.OpenWorkItem(f.session)
	if !ok || !open.Open() {
		t.Fatalf("expected an open item: %#v", open)
	}

	// Second turn extends it; third turn is a different goal.
	f.turn(t, "t2", "Now add the FTS tables", "Tables added.", map[string]string{"b.go": "b\n"})
	f.turn(t, "t3", "Unrelated: fix the README typo", "Fixed.", map[string]string{"README.md": "hello\n"})
	f.reader.replies = []string{reply(
		map[string]any{"id": open.ID, "starts_at_step": "t2", "goal": "Build search", "approach": "Schema first, then FTS tables", "outcome": "FTS tables in", "status": "wip",
			"entities": []map[string]any{}},
		map[string]any{"id": nil, "starts_at_step": "t3", "goal": "Fix README typo", "approach": "Edited README", "outcome": "Typo fixed", "status": "done",
			"entities": []map[string]any{{"type": "file", "name": "README.md", "role": "touched", "confidence": 1, "evidence_step_id": "t3"}}},
	)}
	f.process(t, index.InsightJobKindTurn)

	req := f.reader.requests[1]
	if req.OpenWorkItem == nil || req.OpenWorkItem.ID != open.ID || len(req.Turns) != 2 {
		t.Fatalf("second request: open=%v turns=%d", req.OpenWorkItem, len(req.Turns))
	}

	items, _ := f.proc.Index.ListWorkItems(index.WorkItemFilter{})
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	byGoal := map[string]index.WorkItem{}
	for _, w := range items {
		byGoal[w.Goal] = w
	}
	search := byGoal["Build search"]
	if search.ID != open.ID || search.Approach != "Schema first, then FTS tables" || search.Open() || search.Status != "wip" {
		t.Fatalf("extended item should be closed wip because a later item started: %#v", search)
	}
	steps, _ := f.proc.Index.WorkItemSteps(search.ID)
	if len(steps) != 2 {
		t.Fatalf("search item should span t1 and t2: %v", steps)
	}
	files, _ := f.proc.Index.WorkItemFiles(search.ID)
	if strings.Join(files, ",") != "a.go,b.go" {
		t.Fatalf("search files: %v", files)
	}
	readme := byGoal["Fix README typo"]
	if readme.Status != "done" || readme.EndTS.IsZero() {
		t.Fatalf("readme item: %#v", readme)
	}
	if files, _ := f.proc.Index.WorkItemFiles(readme.ID); strings.Join(files, ",") != "README.md" {
		t.Fatalf("readme files: %v", files)
	}
	if _, ok, _ := f.proc.Index.OpenWorkItem(f.session); ok {
		t.Fatal("no item should be open after a done item")
	}
}

func TestProcess_IdleClosesOpenItemAndOffersItAsResumable(t *testing.T) {
	f := newFixture(t)
	f.proc.Settings.WorkItemIdle = time.Millisecond
	f.turn(t, "t1", "Begin migration", "Started.", map[string]string{"m.go": "m\n"})
	f.reader.replies = []string{reply(map[string]any{"id": nil, "starts_at_step": "t1", "goal": "Migrate", "approach": "", "outcome": "", "status": "wip", "entities": []any{}})}
	f.process(t, index.InsightJobKindTurn)
	open, _, _ := f.proc.Index.OpenWorkItem(f.session)

	time.Sleep(5 * time.Millisecond)
	f.turn(t, "t2", "Continue the migration", "Continued.", map[string]string{"m.go": "mm\n"})
	f.reader.replies = []string{reply(map[string]any{"id": nil, "continues_work_item_id": open.ID, "starts_at_step": "t2", "goal": "Migrate (cont.)", "approach": "", "outcome": "", "status": "done", "entities": []any{}})}
	f.process(t, index.InsightJobKindTurn)

	req := f.reader.requests[1]
	if req.OpenWorkItem != nil {
		t.Fatal("idle item must not be shown as open")
	}
	if len(req.Resumable) != 1 || req.Resumable[0].ID != open.ID {
		t.Fatalf("idle item should be resumable: %#v", req.Resumable)
	}
	items, _ := f.proc.Index.ListWorkItems(index.WorkItemFilter{})
	if len(items) != 2 {
		t.Fatalf("items: %d", len(items))
	}
	for _, w := range items {
		if w.Goal == "Migrate (cont.)" && w.ContinuesWorkItemID != open.ID {
			t.Fatalf("continuation not recorded: %#v", w)
		}
		if w.ID == open.ID && (w.Open() || w.Status != "wip") {
			t.Fatalf("idle-closed item should be closed wip: %#v", w)
		}
	}
}

func TestProcess_SessionJobReplacesAndBatches(t *testing.T) {
	f := newFixture(t)
	f.turn(t, "t1", "one", "1", nil)
	f.turn(t, "t2", "two", "2", nil)
	f.turn(t, "t3", "three", "3", nil)
	f.reader.replies = []string{reply(map[string]any{"id": nil, "starts_at_step": "t1", "goal": "old reading", "status": "done", "entities": []any{}})}
	f.process(t, index.InsightJobKindTurn)

	// Rebuild with a budget that fits one turn per call: three reads,
	// chained through the open item, replacing the old reading.
	f.proc.Settings.Model.MaxInputTokens = 20
	f.reader.calls = 0
	f.reader.requests = nil
	f.reader.replies = []string{
		reply(map[string]any{"id": nil, "starts_at_step": "t1", "goal": "new reading", "approach": "a", "status": "wip", "entities": []any{}}),
		reply(map[string]any{"id": "__open__", "starts_at_step": "t2", "goal": "new reading", "approach": "a b", "status": "wip", "entities": []any{}}),
		reply(map[string]any{"id": "__open__", "starts_at_step": "t3", "goal": "new reading", "approach": "a b c", "status": "done", "entities": []any{}}),
	}
	// The fake cannot know the open id ahead of time; patch replies as it goes.
	orig := f.proc.Reader
	f.proc.Reader = readerFunc(func(ctx context.Context, ins string, req []byte) ([]byte, error) {
		var r Request
		_ = json.Unmarshal(req, &r)
		out, err := orig.Read(ctx, ins, req)
		if r.OpenWorkItem != nil {
			out = []byte(strings.ReplaceAll(string(out), "__open__", r.OpenWorkItem.ID))
		}
		return out, err
	})
	f.process(t, index.InsightJobKindSession)

	if f.reader.calls != 3 {
		t.Fatalf("expected 3 batched reads, got %d", f.reader.calls)
	}
	items, _ := f.proc.Index.ListWorkItems(index.WorkItemFilter{})
	if len(items) != 1 || items[0].Goal != "new reading" || items[0].Approach != "a b c" || items[0].Status != "done" {
		t.Fatalf("items: %#v", items)
	}
}

func TestProcess_UnparseableReplyIsPermanentAfterRetry(t *testing.T) {
	f := newFixture(t)
	f.turn(t, "t1", "hi", "hello", nil)
	f.reader.replies = []string{`{"work_items": []}`}
	err := f.proc.Process(context.Background(), index.InsightJob{Kind: index.InsightJobKindTurn, SessionID: f.session})
	var perm insight.PermanentError
	if err == nil || !errorsAs(err, &perm) {
		t.Fatalf("expected permanent error, got %v", err)
	}
	if f.reader.calls != 2 {
		t.Fatalf("expected one retry, calls=%d", f.reader.calls)
	}
	if items, _ := f.proc.Index.ListWorkItems(index.WorkItemFilter{}); len(items) != 0 {
		t.Fatal("nothing should be written")
	}
}

type readerFunc func(ctx context.Context, instructions string, request []byte) ([]byte, error)

func (r readerFunc) Read(ctx context.Context, i string, q []byte) ([]byte, error) {
	return r(ctx, i, q)
}

func keys(m map[string]index.EntityLink) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func errorsAs(err error, target *insight.PermanentError) bool {
	for err != nil {
		if p, ok := err.(insight.PermanentError); ok {
			*target = p
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
