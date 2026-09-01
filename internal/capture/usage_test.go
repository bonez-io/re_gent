package capture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/store"
)

// usageLine builds a transcript line shaped like the ones Claude Code writes.
func usageLine(requestID string, in, out, cacheCreate, cacheRead int64) string {
	line := map[string]any{
		"type":      "assistant",
		"requestId": requestID,
		"message": map[string]any{
			"id":   "msg_" + requestID,
			"role": "assistant",
			"usage": map[string]any{
				"input_tokens":                in,
				"output_tokens":               out,
				"cache_creation_input_tokens": cacheCreate,
				"cache_read_input_tokens":     cacheRead,
			},
		},
	}
	data, err := json.Marshal(line)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func writeTranscriptFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func newUsageRecorder(t *testing.T, root string) *Recorder {
	t.Helper()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}
	recorder, ok, err := Open(root)
	if err != nil {
		t.Fatalf("open recorder: %v", err)
	}
	if !ok {
		t.Fatal("expected initialized recorder")
	}
	t.Cleanup(func() { _ = recorder.Close() })
	return recorder
}

// runUsageTurn drives one complete prompt/tool/stop cycle, the sequence the Claude
// Code hooks produce for a turn that used a tool.
func runUsageTurn(t *testing.T, recorder *Recorder, meta SessionMetadata, turnID, file string) {
	t.Helper()
	if err := recorder.RecordUserPrompt(UserPrompt{SessionMetadata: meta, TurnID: turnID, Prompt: "do work"}); err != nil {
		t.Fatalf("record prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(recorder.CWD, file), []byte("content\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := recorder.RecordToolUse(ToolUse{
		SessionMetadata: meta,
		TurnID:          turnID,
		ToolName:        "Write",
		ToolUseID:       "call_" + turnID,
		ToolInput:       json.RawMessage(`{"file_path":"` + file + `"}`),
		ToolResponse:    json.RawMessage(`"ok"`),
	}); err != nil {
		t.Fatalf("record tool: %v", err)
	}
	if err := recorder.RecordAssistantAndFinalize(AssistantResponse{
		SessionMetadata: meta, TurnID: turnID, LastAssistantMessage: "done",
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}
}

func stepForTurn(t *testing.T, recorder *Recorder, sessionID, turnID string) *store.Step {
	t.Helper()
	hash, ok, err := recorder.Index.StepForTurn(sessionID, turnID)
	if err != nil {
		t.Fatalf("step for turn %s: %v", turnID, err)
	}
	if !ok {
		t.Fatalf("no step recorded for turn %s", turnID)
	}
	step, err := recorder.Store.ReadStep(hash)
	if err != nil {
		t.Fatalf("read step: %v", err)
	}
	return step
}

func TestRecorder_StepRecordsTranscriptUsage(t *testing.T) {
	root := t.TempDir()
	recorder := newUsageRecorder(t, root)

	transcriptDir := t.TempDir()
	transcript := filepath.Join(transcriptDir, "session.jsonl")
	writeTranscriptFile(t, transcript, usageLine("req_1", 3, 114, 19576, 16601))

	meta := SessionMetadata{
		SessionID:      "usage-session",
		Origin:         OriginClaudeCode,
		TranscriptPath: transcript,
	}
	sessionID := canonicalSessionID(OriginClaudeCode, meta.SessionID)

	runUsageTurn(t, recorder, meta, "turn-1", "one.txt")

	step := stepForTurn(t, recorder, sessionID, "turn-1")
	if step.Usage == nil {
		t.Fatal("expected usage on the first step")
	}
	want := store.Usage{InputTokens: 3, OutputTokens: 114, CacheCreationTokens: 19576, CacheReadTokens: 16601, APICalls: 1}
	if *step.Usage != want {
		t.Fatalf("usage = %+v, want %+v", *step.Usage, want)
	}
	if step.UsageTotal == nil || *step.UsageTotal != want {
		t.Fatalf("usage total = %+v, want %+v", step.UsageTotal, want)
	}

	// The transcript reports session totals, so the second step must record only
	// what the second turn added.
	writeTranscriptFile(t, transcript,
		usageLine("req_1", 3, 114, 19576, 16601),
		usageLine("req_2", 7, 20, 0, 40000),
	)
	runUsageTurn(t, recorder, meta, "turn-2", "two.txt")

	second := stepForTurn(t, recorder, sessionID, "turn-2")
	if second.Usage == nil {
		t.Fatal("expected usage on the second step")
	}
	wantDelta := store.Usage{InputTokens: 7, OutputTokens: 20, CacheReadTokens: 40000, APICalls: 1}
	if *second.Usage != wantDelta {
		t.Fatalf("second step usage = %+v, want %+v", *second.Usage, wantDelta)
	}
	wantTotal := store.Usage{InputTokens: 10, OutputTokens: 134, CacheCreationTokens: 19576, CacheReadTokens: 56601, APICalls: 2}
	if *second.UsageTotal != wantTotal {
		t.Fatalf("second step total = %+v, want %+v", *second.UsageTotal, wantTotal)
	}
}

func TestRecorder_StepUsageIncludesSubagentTranscripts(t *testing.T) {
	root := t.TempDir()
	recorder := newUsageRecorder(t, root)

	transcriptDir := t.TempDir()
	transcript := filepath.Join(transcriptDir, "session.jsonl")
	writeTranscriptFile(t, transcript, usageLine("req_main", 1, 2, 0, 0))
	writeTranscriptFile(t, filepath.Join(transcriptDir, "session", "subagents", "agent-aaa.jsonl"),
		usageLine("req_sub", 10, 20, 0, 0))

	meta := SessionMetadata{SessionID: "sub-session", Origin: OriginClaudeCode, TranscriptPath: transcript}
	sessionID := canonicalSessionID(OriginClaudeCode, meta.SessionID)

	runUsageTurn(t, recorder, meta, "turn-1", "one.txt")

	step := stepForTurn(t, recorder, sessionID, "turn-1")
	want := store.Usage{InputTokens: 11, OutputTokens: 22, APICalls: 2, Subagents: 1}
	if step.Usage == nil || *step.Usage != want {
		t.Fatalf("usage = %+v, want %+v", step.Usage, want)
	}

	steps, err := recorder.Index.ListSteps(sessionID, 10)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 indexed step, got %d", len(steps))
	}
	if steps[0].Usage != want {
		t.Fatalf("indexed usage = %+v, want %+v", steps[0].Usage, want)
	}
}

// A host that starts a fresh transcript (after /compact, for example) reports
// totals below the baseline. That must read as "nothing new", never as a
// negative count.
func TestRecorder_TranscriptResetYieldsNoNegativeUsage(t *testing.T) {
	root := t.TempDir()
	recorder := newUsageRecorder(t, root)

	transcriptDir := t.TempDir()
	transcript := filepath.Join(transcriptDir, "session.jsonl")
	writeTranscriptFile(t, transcript,
		usageLine("req_1", 100, 200, 300, 400),
		usageLine("req_2", 100, 200, 300, 400),
	)

	meta := SessionMetadata{SessionID: "reset-session", Origin: OriginClaudeCode, TranscriptPath: transcript}
	sessionID := canonicalSessionID(OriginClaudeCode, meta.SessionID)
	runUsageTurn(t, recorder, meta, "turn-1", "one.txt")

	// The host replaces the transcript with a fresh, much smaller one.
	writeTranscriptFile(t, transcript, usageLine("req_new", 5, 6, 0, 0))
	runUsageTurn(t, recorder, meta, "turn-2", "two.txt")

	second := stepForTurn(t, recorder, sessionID, "turn-2")
	if second.Usage != nil {
		t.Fatalf("expected no usage after a transcript reset, got %+v", *second.Usage)
	}
	// The baseline follows the new transcript so later turns measure against it.
	wantTotal := store.Usage{InputTokens: 5, OutputTokens: 6, APICalls: 1}
	if second.UsageTotal == nil || *second.UsageTotal != wantTotal {
		t.Fatalf("usage total = %+v, want %+v", second.UsageTotal, wantTotal)
	}

	writeTranscriptFile(t, transcript, usageLine("req_new", 5, 6, 0, 0), usageLine("req_next", 1, 1, 0, 0))
	runUsageTurn(t, recorder, meta, "turn-3", "three.txt")

	third := stepForTurn(t, recorder, sessionID, "turn-3")
	wantDelta := store.Usage{InputTokens: 1, OutputTokens: 1, APICalls: 1}
	if third.Usage == nil || *third.Usage != wantDelta {
		t.Fatalf("third step usage = %+v, want %+v", third.Usage, wantDelta)
	}
}

// A turn whose transcript was briefly unreadable records no baseline. The turn
// after it must still charge only what is new, not the whole session again.
func TestRecorder_UsageBaselineSurvivesAStepWithoutATranscript(t *testing.T) {
	root := t.TempDir()
	recorder := newUsageRecorder(t, root)

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	writeTranscriptFile(t, transcript, usageLine("req_1", 100, 200, 0, 0))

	meta := SessionMetadata{SessionID: "gap-session", Origin: OriginClaudeCode, TranscriptPath: transcript}
	sessionID := canonicalSessionID(OriginClaudeCode, meta.SessionID)
	runUsageTurn(t, recorder, meta, "turn-1", "one.txt")

	// The transcript disappears for one turn, then comes back with more usage.
	if err := os.Rename(transcript, transcript+".away"); err != nil {
		t.Fatalf("hide transcript: %v", err)
	}
	runUsageTurn(t, recorder, meta, "turn-2", "two.txt")
	if gap := stepForTurn(t, recorder, sessionID, "turn-2"); gap.UsageTotal != nil {
		t.Fatalf("expected no baseline on the gap step, got %+v", *gap.UsageTotal)
	}

	if err := os.Rename(transcript+".away", transcript); err != nil {
		t.Fatalf("restore transcript: %v", err)
	}
	writeTranscriptFile(t, transcript, usageLine("req_1", 100, 200, 0, 0), usageLine("req_2", 1, 2, 0, 0))
	runUsageTurn(t, recorder, meta, "turn-3", "three.txt")

	third := stepForTurn(t, recorder, sessionID, "turn-3")
	wantDelta := store.Usage{InputTokens: 1, OutputTokens: 2, APICalls: 1}
	if third.Usage == nil || *third.Usage != wantDelta {
		t.Fatalf("usage = %+v, want %+v (turn 1's usage must not be charged twice)", third.Usage, wantDelta)
	}
}

func TestRecorder_MissingTranscriptStillRecordsTheStep(t *testing.T) {
	root := t.TempDir()
	recorder := newUsageRecorder(t, root)

	meta := SessionMetadata{
		SessionID:      "missing-transcript",
		Origin:         OriginClaudeCode,
		TranscriptPath: filepath.Join(t.TempDir(), "not-written-yet.jsonl"),
	}
	sessionID := canonicalSessionID(OriginClaudeCode, meta.SessionID)

	runUsageTurn(t, recorder, meta, "turn-1", "one.txt")

	step := stepForTurn(t, recorder, sessionID, "turn-1")
	if step.Usage != nil || step.UsageTotal != nil {
		t.Fatalf("expected no usage without a readable transcript, got %+v / %+v", step.Usage, step.UsageTotal)
	}
	if len(step.Causes) != 1 {
		t.Fatalf("expected the step to keep its cause, got %d", len(step.Causes))
	}
}

func TestRecorder_NoTranscriptPathRecordsNoUsage(t *testing.T) {
	root := t.TempDir()
	recorder := newUsageRecorder(t, root)

	meta := SessionMetadata{SessionID: "no-transcript", Origin: OriginClaudeCode}
	sessionID := canonicalSessionID(OriginClaudeCode, meta.SessionID)

	runUsageTurn(t, recorder, meta, "turn-1", "one.txt")

	step := stepForTurn(t, recorder, sessionID, "turn-1")
	if step.Usage != nil || step.UsageTotal != nil {
		t.Fatalf("expected no usage fields, got %+v / %+v", step.Usage, step.UsageTotal)
	}

	// Nothing went wrong, so nothing should have been logged as an error.
	if data, err := os.ReadFile(filepath.Join(root, ".regent", "log", "hook-error.log")); err == nil {
		t.Fatalf("unexpected hook errors: %s", data)
	}
}

func TestRecorder_UnparseableTranscriptRecordsNoUsage(t *testing.T) {
	root := t.TempDir()
	recorder := newUsageRecorder(t, root)

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	writeTranscriptFile(t, transcript, "this is not jsonl", "{neither is this")

	meta := SessionMetadata{SessionID: "garbage-transcript", Origin: OriginClaudeCode, TranscriptPath: transcript}
	sessionID := canonicalSessionID(OriginClaudeCode, meta.SessionID)

	runUsageTurn(t, recorder, meta, "turn-1", "one.txt")

	step := stepForTurn(t, recorder, sessionID, "turn-1")
	if step.Usage != nil || step.UsageTotal != nil {
		t.Fatalf("expected no usage from an unparseable transcript, got %+v / %+v", step.Usage, step.UsageTotal)
	}
}

// Hook logs live in the repo and get read by humans and shipped in bug reports.
// A failure while reading the transcript must never spill its contents there.
func TestRecorder_HookLogsCarryNoTranscriptContent(t *testing.T) {
	const secret = "sk-ant-DO-NOT-LEAK-THIS"

	root := t.TempDir()
	recorder := newUsageRecorder(t, root)

	transcriptDir := t.TempDir()
	transcript := filepath.Join(transcriptDir, "session.jsonl")
	writeTranscriptFile(t, transcript,
		`{"type":"user","message":{"role":"user","content":"the key is `+secret+`"}}`,
		usageLine("req_1", 1, 2, 0, 0),
	)
	// A file where the subagents directory belongs forces the failure path.
	if err := os.MkdirAll(filepath.Join(transcriptDir, "session"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, "session", "subagents"), []byte(secret), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	meta := SessionMetadata{SessionID: "secret-session", Origin: OriginClaudeCode, TranscriptPath: transcript}
	sessionID := canonicalSessionID(OriginClaudeCode, meta.SessionID)

	runUsageTurn(t, recorder, meta, "turn-1", "one.txt")

	// The readable part of the transcript is still accounted for.
	step := stepForTurn(t, recorder, sessionID, "turn-1")
	want := store.Usage{InputTokens: 1, OutputTokens: 2, APICalls: 1}
	if step.Usage == nil || *step.Usage != want {
		t.Fatalf("usage = %+v, want %+v", step.Usage, want)
	}

	logDir := filepath.Join(root, ".regent", "log")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("expected the failure to be logged: %v", err)
	}
	loggedFailure := false
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(logDir, entry.Name()))
		if err != nil {
			t.Fatalf("read log %s: %v", entry.Name(), err)
		}
		if strings.Contains(string(data), secret) {
			t.Fatalf("%s leaked transcript content: %s", entry.Name(), data)
		}
		if strings.Contains(string(data), "collect transcript usage") {
			loggedFailure = true
		}
	}
	if !loggedFailure {
		t.Fatal("expected the transcript failure to be logged")
	}
}
