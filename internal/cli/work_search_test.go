package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/store"
	"github.com/spf13/cobra"
)

func seedCLIWorkItems(t *testing.T) *store.Store {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	withWorkingDir(t, root)
	s, err := store.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.Open(s)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	now := time.Now()
	if err := idx.UpsertSession(index.SessionUpdate{ID: "sess-a", Origin: "claude_code"}); err != nil {
		t.Fatal(err)
	}
	if err := idx.AppendMessage(index.Message{ID: "m1", SessionID: "sess-unread", Timestamp: 1, MessageType: "user", ContentText: "investigate the flaky retry in the uploader"}); err != nil {
		t.Fatal(err)
	}
	if err := idx.WriteInsight(index.InsightWrite{Items: []index.WorkItemWrite{
		{Item: index.WorkItem{ID: "wi_done1", SessionID: "sess-a", Origin: "claude_code", StartTS: now.Add(-2 * time.Hour), EndTS: now.Add(-time.Hour),
			Goal: "Fix flaky retry loop", Approach: "Backoff", Outcome: "Fixed", Status: index.WorkItemDone, ModelProvider: "anthropic", ModelName: "haiku", PromptVersion: "1"},
			StepIDs: []string{"step1"}, Files: []string{"queue.go"},
			Links: []index.LinkWrite{{Type: "pull_request", Name: "bonez-io/re_gent#95", Ref: "https://github.com/bonez-io/re_gent/pull/95", Role: "produced", Confidence: 1, Source: "deterministic", EvidenceStepID: "step1"}}},
		{Item: index.WorkItem{ID: "wi_wip1", SessionID: "sess-b", Origin: "codex_cli", StartTS: now,
			Goal: "Make retry configurable", Approach: "", Outcome: "", Status: index.WorkItemWIP, ModelProvider: "anthropic", ModelName: "haiku", PromptVersion: "1"},
			StepIDs: []string{"step2"}, Files: []string{"queue.go", "config.go"}},
	}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func runCmd(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestWorkListAndShow(t *testing.T) {
	seedCLIWorkItems(t)
	out, err := runCmd(t, WorkCmd(), "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Make retry configurable") || !strings.Contains(out, "Fix flaky retry loop") {
		t.Fatalf("list output:\n%s", out)
	}
	if strings.Index(out, "Make retry configurable") > strings.Index(out, "Fix flaky retry loop") {
		t.Fatalf("newest first:\n%s", out)
	}
	out, _ = runCmd(t, WorkCmd(), "list", "--status", "wip")
	if strings.Contains(out, "Fix flaky") {
		t.Fatalf("status filter:\n%s", out)
	}
	if _, err := runCmd(t, WorkCmd(), "list", "--status", "bogus"); err == nil {
		t.Fatal("bad status must error")
	}

	out, err = runCmd(t, WorkCmd(), "show", "wi_done1")
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	for _, want := range []string{"Fix flaky retry loop", "Backoff", "pull_request", "bonez-io/re_gent#95", "queue.go", "rgt show step1", "anthropic haiku", "prompt v1"} {
		if !strings.Contains(out, want) {
			t.Errorf("show missing %q:\n%s", want, out)
		}
	}
	out, err = runCmd(t, WorkCmd(), "show", "wi_wip1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var j struct {
		Open  bool     `json:"open"`
		Files []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &j); err != nil || !j.Open || len(j.Files) != 2 {
		t.Fatalf("json: %v %s", err, out)
	}
	if _, err := runCmd(t, WorkCmd(), "show", "nope"); err == nil {
		t.Fatal("unknown id must error")
	}
}

func TestSearch_TextFileEntityAndUnread(t *testing.T) {
	seedCLIWorkItems(t)

	out, err := runCmd(t, SearchCmd(), "flaky retry")
	if err != nil {
		t.Fatalf("search: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Fix flaky retry loop") {
		t.Fatalf("text hit missing:\n%s", out)
	}
	if !strings.Contains(out, "Not yet read") || !strings.Contains(out, "sess-unread") {
		t.Fatalf("unread session must be listed:\n%s", out)
	}

	out, _ = runCmd(t, SearchCmd(), "--file", "queue.go")
	if !strings.Contains(out, "Make retry configurable") || !strings.Contains(out, "Fix flaky retry loop") {
		t.Fatalf("file search:\n%s", out)
	}
	if strings.Index(out, "Make retry configurable") > strings.Index(out, "Fix flaky retry loop") {
		t.Fatalf("wip should rank above done at equal score:\n%s", out)
	}
	out, _ = runCmd(t, SearchCmd(), "--file", "config.go", "--status", "done")
	if !strings.Contains(out, "Nothing matched") {
		t.Fatalf("filters should intersect:\n%s", out)
	}
	out, _ = runCmd(t, SearchCmd(), "--entity", "bonez-io/re_gent#95")
	if !strings.Contains(out, "Fix flaky retry loop") || strings.Contains(out, "Make retry configurable") {
		t.Fatalf("entity search:\n%s", out)
	}
	out, _ = runCmd(t, SearchCmd(), "retry", "--file", "queue.go", "--json")
	var j struct {
		Hits []struct {
			Matched []string `json:"matched"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(out), &j); err != nil || len(j.Hits) != 2 {
		t.Fatalf("json: %v %s", err, out)
	}
	if _, err := runCmd(t, SearchCmd()); err == nil {
		t.Fatal("no query and no filter must error")
	}
}
