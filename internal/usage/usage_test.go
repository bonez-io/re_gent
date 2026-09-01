package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/store"
)

// assistantLine builds a transcript line shaped like the ones Claude Code
// writes: a nested API message carrying a usage block, tagged with the id of
// the request that produced it.
func assistantLine(requestID, messageID string, in, out, cacheCreate, cacheRead int64) string {
	line := map[string]any{
		"type":      "assistant",
		"requestId": requestID,
		"uuid":      messageID + "-uuid",
		"message": map[string]any{
			"id":   messageID,
			"role": "assistant",
			"content": []map[string]any{
				{"type": "text", "text": "some assistant prose"},
			},
			"usage": map[string]any{
				"input_tokens":                in,
				"output_tokens":               out,
				"cache_creation_input_tokens": cacheCreate,
				"cache_read_input_tokens":     cacheRead,
				"service_tier":                "standard",
			},
		},
	}
	data, err := json.Marshal(line)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func writeTranscript(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func TestCollect_SumsTokensCacheAndAPICalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeTranscript(t, path,
		`{"type":"user","message":{"role":"user","content":"do the thing"}}`,
		assistantLine("req_1", "msg_1", 3, 114, 19576, 16601),
		`{"type":"attachment","attachment":{"type":"file","content":"no usage here"}}`,
		assistantLine("req_2", "msg_2", 5, 220, 0, 36177),
	)

	report, err := Collect(path)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	want := store.Usage{
		InputTokens:         8,
		OutputTokens:        334,
		CacheCreationTokens: 19576,
		CacheReadTokens:     52778,
		APICalls:            2,
	}
	if report.Total != want {
		t.Fatalf("total = %+v, want %+v", report.Total, want)
	}
	if report.Main != want {
		t.Fatalf("main = %+v, want %+v", report.Main, want)
	}
}

// One API response is split across several transcript lines when it carries
// several content blocks, and each line repeats the same usage block. Summing
// lines instead of requests would report several times the real cost.
func TestCollect_CountsEachRequestOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeTranscript(t, path,
		assistantLine("req_1", "msg_1", 10, 100, 200, 300),
		assistantLine("req_1", "msg_1", 10, 100, 200, 300),
		assistantLine("req_1", "msg_1", 10, 100, 200, 300),
		assistantLine("req_2", "msg_2", 1, 2, 3, 4),
	)

	report, err := Collect(path)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	want := store.Usage{InputTokens: 11, OutputTokens: 102, CacheCreationTokens: 203, CacheReadTokens: 304, APICalls: 2}
	if report.Total != want {
		t.Fatalf("total = %+v, want %+v", report.Total, want)
	}
}

// Older transcripts have no requestId; the API message id identifies the call.
func TestCollect_FallsBackToMessageIDForDeduplication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeTranscript(t, path,
		assistantLine("", "msg_1", 10, 20, 0, 0),
		assistantLine("", "msg_1", 10, 20, 0, 0),
		assistantLine("", "msg_2", 1, 2, 0, 0),
	)

	report, err := Collect(path)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	want := store.Usage{InputTokens: 11, OutputTokens: 22, APICalls: 2}
	if report.Total != want {
		t.Fatalf("total = %+v, want %+v", report.Total, want)
	}
}

func TestCollect_RecursesIntoSubagentTranscripts(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "session.jsonl")
	writeTranscript(t, main, assistantLine("req_main", "msg_main", 1, 2, 3, 4))

	// A subagent of the session, and a subagent of that subagent.
	child := filepath.Join(dir, "session", "subagents", "agent-aaa.jsonl")
	writeTranscript(t, child, assistantLine("req_child", "msg_child", 10, 20, 30, 40))
	if err := os.WriteFile(
		filepath.Join(dir, "session", "subagents", "agent-aaa.meta.json"),
		[]byte(`{"agentType":"general-purpose","spawnDepth":1}`), 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	grandchild := filepath.Join(dir, "session", "subagents", "agent-aaa", "subagents", "agent-bbb.jsonl")
	writeTranscript(t, grandchild, assistantLine("req_grandchild", "msg_grandchild", 100, 200, 300, 400))

	report, err := Collect(main)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	wantTotal := store.Usage{
		InputTokens:         111,
		OutputTokens:        222,
		CacheCreationTokens: 333,
		CacheReadTokens:     444,
		APICalls:            3,
		Subagents:           2,
	}
	if report.Total != wantTotal {
		t.Fatalf("total = %+v, want %+v", report.Total, wantTotal)
	}
	wantMain := store.Usage{InputTokens: 1, OutputTokens: 2, CacheCreationTokens: 3, CacheReadTokens: 4, APICalls: 1}
	if report.Main != wantMain {
		t.Fatalf("main = %+v, want %+v", report.Main, wantMain)
	}

	if len(report.Subagents) != 2 {
		t.Fatalf("expected 2 subagents, got %d: %+v", len(report.Subagents), report.Subagents)
	}
	byID := map[string]Subagent{}
	for _, sub := range report.Subagents {
		byID[sub.ID] = sub
	}
	if got := byID["aaa"]; got.Depth != 1 || got.Type != "general-purpose" || got.Usage.InputTokens != 10 {
		t.Fatalf("subagent aaa = %+v, want depth 1, type general-purpose, 10 input tokens", got)
	}
	if got := byID["bbb"]; got.Depth != 2 || got.Usage.InputTokens != 100 {
		t.Fatalf("subagent bbb = %+v, want depth 2 and 100 input tokens", got)
	}
}

func TestCollect_StopsRecursingAtMaxDepth(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "session.jsonl")
	writeTranscript(t, main, assistantLine("req_main", "msg_main", 1, 0, 0, 0))

	// Nest one level deeper than the recursion bound allows.
	path := filepath.Join(dir, "session")
	for depth := 1; depth <= maxSubagentDepth+1; depth++ {
		name := fmt.Sprintf("agent-%d", depth)
		transcript := filepath.Join(path, "subagents", name+".jsonl")
		writeTranscript(t, transcript, assistantLine(fmt.Sprintf("req_%d", depth), fmt.Sprintf("msg_%d", depth), 1, 0, 0, 0))
		path = filepath.Join(path, "subagents", name)
	}

	report, err := Collect(main)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if report.Total.Subagents != int64(maxSubagentDepth) {
		t.Fatalf("subagents = %d, want %d (recursion bound)", report.Total.Subagents, maxSubagentDepth)
	}
}

func TestCollect_EmptyPathIsNotAnError(t *testing.T) {
	report, err := Collect("")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !report.Total.IsZero() || len(report.Subagents) != 0 {
		t.Fatalf("expected empty report, got %+v", report)
	}
}

func TestCollect_MissingTranscriptReportsErrorAndNoUsage(t *testing.T) {
	report, err := Collect(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err == nil {
		t.Fatal("expected an error for a missing transcript")
	}
	if !report.Total.IsZero() {
		t.Fatalf("expected no usage, got %+v", report.Total)
	}
}

// The host appends to the transcript while we read it, so a torn or truncated
// line is routine. Everything readable must still be counted.
func TestCollect_SkipsUnparseableLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeTranscript(t, path,
		assistantLine("req_1", "msg_1", 7, 8, 0, 0),
		`{"type":"assistant","message":{"id":"msg_torn","usage":{"input_toke`,
		``,
		`not json at all`,
		`{"type":"assistant","message":"a string, not an object"}`,
		assistantLine("req_2", "msg_2", 1, 1, 0, 0),
	)

	report, err := Collect(path)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := store.Usage{InputTokens: 8, OutputTokens: 9, APICalls: 2}
	if report.Total != want {
		t.Fatalf("total = %+v, want %+v", report.Total, want)
	}
}

func TestCollect_EntirelyUnparseableTranscriptYieldsNoUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("\x00\x01\x02 not jsonl at all\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	report, err := Collect(path)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !report.Total.IsZero() {
		t.Fatalf("expected no usage, got %+v", report.Total)
	}
}

// Transcript lines embed whole files, so they routinely exceed bufio's default
// buffer. A long line must be read whole, not truncated or abandoned.
func TestCollect_ReadsLinesLargerThanTheReadBuffer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	padded := map[string]any{
		"type":      "assistant",
		"requestId": "req_1",
		"padding":   strings.Repeat("x", 512*1024),
		"message": map[string]any{
			"id":    "msg_1",
			"usage": map[string]any{"input_tokens": 42, "output_tokens": 7},
		},
	}
	data, err := json.Marshal(padded)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	writeTranscript(t, path, string(data), assistantLine("req_2", "msg_2", 1, 1, 0, 0))

	report, err := Collect(path)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := store.Usage{InputTokens: 43, OutputTokens: 8, APICalls: 2}
	if report.Total != want {
		t.Fatalf("total = %+v, want %+v", report.Total, want)
	}
}

func TestCollect_DropsLinesBeyondTheSizeCapAndKeepsReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	oversized := `{"type":"assistant","padding":"` + strings.Repeat("x", maxLineBytes+1024) + `"}`
	writeTranscript(t, path, oversized, assistantLine("req_after", "msg_after", 9, 9, 0, 0))

	report, err := Collect(path)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := store.Usage{InputTokens: 9, OutputTokens: 9, APICalls: 1}
	if report.Total != want {
		t.Fatalf("total = %+v, want %+v", report.Total, want)
	}
}

// A broken subagent transcript is reported, but must not discard the usage we
// did manage to read.
func TestCollect_UnreadableSubagentDirectoryKeepsMainUsage(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "session.jsonl")
	writeTranscript(t, main, assistantLine("req_main", "msg_main", 5, 6, 0, 0))

	// A file where the subagents directory is expected.
	if err := os.MkdirAll(filepath.Join(dir, "session"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session", "subagents"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	report, err := Collect(main)
	if err == nil {
		t.Fatal("expected the unreadable subagent directory to be reported")
	}
	want := store.Usage{InputTokens: 5, OutputTokens: 6, APICalls: 1}
	if report.Total != want {
		t.Fatalf("total = %+v, want %+v", report.Total, want)
	}
}

func TestCollect_IgnoresNonSubagentFiles(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "session.jsonl")
	writeTranscript(t, main, assistantLine("req_main", "msg_main", 1, 0, 0, 0))

	subagents := filepath.Join(dir, "session", "subagents")
	writeTranscript(t, filepath.Join(subagents, "notes.txt"), "ignore me")
	writeTranscript(t, filepath.Join(subagents, "agent-.jsonl"), assistantLine("req_x", "msg_x", 99, 0, 0, 0))
	if err := os.MkdirAll(filepath.Join(subagents, "agent-dir.jsonl"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	report, err := Collect(main)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if report.Total.Subagents != 0 || report.Total.InputTokens != 1 {
		t.Fatalf("total = %+v, want only the main transcript's usage", report.Total)
	}
}

// The transcript is full of prompts, file contents and tool output. Nothing but
// numeric counters may leave this package.
func TestCollect_ReturnsNoTranscriptContent(t *testing.T) {
	const secret = "sk-ant-DO-NOT-LEAK-THIS"

	dir := t.TempDir()
	main := filepath.Join(dir, "session.jsonl")
	line := map[string]any{
		"type":      "assistant",
		"requestId": "req_1",
		"cwd":       "/home/someone/private",
		"message": map[string]any{
			"id":      "msg_1",
			"content": []map[string]any{{"type": "text", "text": "the token is " + secret}},
			"usage":   map[string]any{"input_tokens": 1, "output_tokens": 2},
		},
	}
	data, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	writeTranscript(t, main,
		string(data),
		`{"type":"user","message":{"role":"user","content":"export AWS_SECRET_ACCESS_KEY=`+secret+`"}}`,
	)
	writeTranscript(t, filepath.Join(dir, "session", "subagents", "agent-aaa.jsonl"),
		`{"type":"user","message":{"role":"user","content":"`+secret+`"}}`)

	report, err := Collect(main)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("report leaked transcript content: %s", encoded)
	}
	if strings.Contains(fmt.Sprintf("%+v", report), secret) {
		t.Fatalf("formatted report leaked transcript content")
	}
}

func TestCollect_ErrorsCarryNoTranscriptContent(t *testing.T) {
	const secret = "sk-ant-DO-NOT-LEAK-THIS"

	dir := t.TempDir()
	main := filepath.Join(dir, "session.jsonl")
	writeTranscript(t, main, `{"type":"user","message":{"role":"user","content":"`+secret+`"}}`)
	// A file where the subagents directory belongs: forces the error path.
	if err := os.MkdirAll(filepath.Join(dir, "session"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session", "subagents"), []byte(secret), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := Collect(main)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked transcript content: %v", err)
	}
}

func TestSubagentID(t *testing.T) {
	tests := []struct {
		name   string
		file   string
		want   string
		wantOK bool
	}{
		{name: "subagent transcript", file: "agent-a8fea92c876988695.jsonl", want: "a8fea92c876988695", wantOK: true},
		{name: "meta sidecar", file: "agent-a8fea92c876988695.meta.json", wantOK: false},
		{name: "empty id", file: "agent-.jsonl", wantOK: false},
		{name: "unrelated file", file: "session.jsonl", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := subagentID(tc.file)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("subagentID(%q) = (%q, %v), want (%q, %v)", tc.file, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
