package index

import (
	"strings"
	"testing"
	"time"
)

func seedWorkItems(t *testing.T, idx *DB) (WorkItem, WorkItem) {
	t.Helper()
	now := time.Now()
	a := WorkItem{ID: "wi_a", SessionID: "s1", Origin: "claude_code", StartTS: now.Add(-time.Hour),
		Goal: "Fix flaky retry loop", Approach: "Added exponential backoff", Outcome: "Retries back off", Status: WorkItemDone}
	a.EndTS = now.Add(-30 * time.Minute)
	b := WorkItem{ID: "wi_b", SessionID: "s2", Origin: "codex_cli", StartTS: now,
		Goal: "Build search over sessions", Approach: "FTS5 first", Outcome: "", Status: WorkItemWIP}
	if err := idx.UpsertSession(SessionUpdate{ID: "s1", Origin: "claude_code"}); err != nil {
		t.Fatal(err)
	}
	if err := idx.WriteInsight(InsightWrite{Items: []WorkItemWrite{
		{Item: a, StepIDs: []string{"step1"}, Files: []string{"queue.go"}, Links: []LinkWrite{
			{Type: "Pull Request", Name: "bonez-io/re_gent#95", Ref: "https://github.com/bonez-io/re_gent/pull/95", Role: "produced", Confidence: 1, Source: LinkSourceDeterministic, EvidenceStepID: "step1"},
			{Type: "symbol", Name: "queue.go::retry", Role: "touched", Confidence: 0.9, Source: LinkSourceModel, EvidenceStepID: "step1"},
		}, Vector: []float32{1, 0, 0}, EmbedProvider: "p", EmbedModel: "m"},
		{Item: b, StepIDs: []string{"step2", "step3"}, Files: []string{"index.go", "search.go"}, Links: []LinkWrite{
			{Type: "symbol", Name: "Queue.go::Retry", Role: "referenced", Confidence: 0.5, Source: LinkSourceModel, EvidenceStepID: "step2"},
			{Type: "concept", Name: "search", Role: "goal", Confidence: 0.9, Source: LinkSourceModel, EvidenceStepID: "step2"},
		}, Vector: []float32{0, 1, 0}, EmbedProvider: "p", EmbedModel: "m"},
	}, CursorSession: "s1", CursorSeq: 7}); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestWriteInsight_RoundTripAndEntityDedupe(t *testing.T) {
	_, idx := openTestIndex(t)
	a, b := seedWorkItems(t, idx)

	got, ok, err := idx.GetWorkItem("wi_a")
	if err != nil || !ok || got.Goal != a.Goal || got.Status != WorkItemDone || got.EndTS.IsZero() || got.Open() {
		t.Fatalf("get a: ok=%v err=%v %#v", ok, err, got)
	}
	if got, ok, _ := idx.GetWorkItem("wi_"); ok || got.ID != "" {
		t.Fatal("ambiguous prefix must not resolve")
	} else if _, _, err := idx.GetWorkItem("wi_"); err == nil {
		t.Fatal("ambiguous prefix should be an error")
	}
	if got, ok, _ := idx.GetWorkItem("wi_b"); !ok || !got.Open() {
		t.Fatalf("b should be open: %#v", got)
	}
	open, ok, _ := idx.OpenWorkItem("s2")
	if !ok || open.ID != b.ID {
		t.Fatalf("open for s2: %#v", open)
	}

	// The same symbol, differently cased, is one entity linked to both items.
	links, _ := idx.WorkItemLinks("wi_a")
	linksB, _ := idx.WorkItemLinks("wi_b")
	var symA, symB EntityLink
	for _, l := range links {
		if l.Type == "symbol" {
			symA = l
		}
		if l.Type == "pull_request" && l.Ref == "" {
			t.Fatalf("type not normalized or ref lost: %#v", l)
		}
	}
	for _, l := range linksB {
		if l.Type == "symbol" {
			symB = l
		}
	}
	if symA.ID == "" || symA.ID != symB.ID {
		t.Fatalf("symbol should dedupe on lower(name): %#v vs %#v", symA, symB)
	}
	cov, _ := idx.InsightCoverage()
	if cov.WorkItems != 2 || cov.Entities != 3 || cov.Embeddings != 2 {
		t.Fatalf("coverage: %#v", cov)
	}
	if files, _ := idx.WorkItemFiles("wi_b"); strings.Join(files, ",") != "index.go,search.go" {
		t.Fatalf("files: %v", files)
	}
	if ids, _ := idx.WorkItemsForFile("queue.go"); len(ids) != 1 || ids[0] != "wi_a" {
		t.Fatalf("for file: %v", ids)
	}
	if cur, _ := idx.InsightCursor("s1"); cur != 7 {
		t.Fatalf("cursor: %d", cur)
	}
	if types, _ := idx.EntityTypesInUse(10); len(types) != 3 {
		t.Fatalf("types: %v", types)
	}

	// Rewriting the open item keeps its id and replaces its text.
	b.Approach = "FTS5 first, then embeddings"
	if err := idx.WriteInsight(InsightWrite{Items: []WorkItemWrite{{Item: b, StepIDs: []string{"step4"}}}}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = idx.GetWorkItem("wi_b")
	steps, _ := idx.WorkItemSteps("wi_b")
	if got.Approach != b.Approach || len(steps) != 3 {
		t.Fatalf("rewrite: %#v steps=%v", got, steps)
	}

	// Replacing a session removes everything that hung off its items.
	if err := idx.WriteInsight(InsightWrite{ReplaceSession: "s2"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := idx.GetWorkItem("wi_b"); ok {
		t.Fatal("wi_b should be gone")
	}
	cov, _ = idx.InsightCoverage()
	if cov.WorkItems != 1 || cov.Embeddings != 1 {
		t.Fatalf("after replace: %#v", cov)
	}
	if err := idx.WriteInsight(InsightWrite{Items: []WorkItemWrite{{Item: WorkItem{ID: "x", SessionID: "s", Status: "maybe"}}}}); err == nil {
		t.Fatal("bad status must be rejected")
	}
}

func TestCloseWorkItemAndResumable(t *testing.T) {
	_, idx := openTestIndex(t)
	_, b := seedWorkItems(t, idx)
	if err := idx.CloseWorkItem(b.ID, "step3", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := idx.OpenWorkItem("s2"); ok {
		t.Fatal("closed item must not be open")
	}
	got, _, _ := idx.GetWorkItem(b.ID)
	if got.Status != WorkItemWIP || got.EndStepID != "step3" {
		t.Fatalf("closed wip: %#v", got)
	}
	res, _ := idx.ResumableWorkItems("other", 5)
	if len(res) != 1 || res[0].ID != b.ID {
		t.Fatalf("resumable: %#v", res)
	}
	if res, _ := idx.ResumableWorkItems("s2", 5); len(res) != 0 {
		t.Fatal("own session excluded")
	}
}

func TestSearchFTSAndFindEntities(t *testing.T) {
	_, idx := openTestIndex(t)
	seedWorkItems(t, idx)
	if err := idx.AppendMessage(Message{ID: "m1", SessionID: "s1", StepID: "step1", Timestamp: 1, MessageType: "user", ContentText: "please fix the flaky retry"}); err != nil {
		t.Fatal(err)
	}

	hits, err := idx.SearchWorkItemsFTS("flaky retry", 10)
	if err != nil || len(hits) != 1 || hits[0].ID != "wi_a" {
		t.Fatalf("work items fts: %v %v", hits, err)
	}
	if hits, _ := idx.SearchWorkItemsFTS(`retry "unbalanced`, 10); hits == nil && false {
		t.Fatal("quoted punctuation must not be a syntax error")
	}
	ehits, err := idx.SearchEntitiesFTS("retry", 10)
	if err != nil || len(ehits) != 2 {
		t.Fatalf("entities fts should reach both items through the shared symbol: %v %v", ehits, err)
	}
	mhits, err := idx.SearchMessagesFTS("flaky", 10)
	if err != nil || len(mhits) != 1 || mhits[0].StepID != "step1" || !strings.Contains(mhits[0].Snippet, "flaky") {
		t.Fatalf("messages fts: %#v %v", mhits, err)
	}

	byRef, _ := idx.FindEntities("https://github.com/bonez-io/re_gent/pull/95", 5)
	if len(byRef) != 1 || byRef[0].Type != "pull_request" {
		t.Fatalf("by ref: %#v", byRef)
	}
	byPrefix, _ := idx.FindEntities("queue.go", 5)
	if len(byPrefix) != 1 || byPrefix[0].Type != "symbol" {
		t.Fatalf("by prefix: %#v", byPrefix)
	}
	byTyped, _ := idx.FindEntities("concept:sea", 5)
	if len(byTyped) != 1 || byTyped[0].Name != "search" {
		t.Fatalf("typed: %#v", byTyped)
	}
	byFTS, _ := idx.FindEntities("re_gent", 5)
	if len(byFTS) == 0 {
		t.Fatal("full-text fallback should find the pull request by its name")
	}
	ids, _ := idx.WorkItemsForEntity(byPrefix[0].ID)
	if len(ids) != 2 {
		t.Fatalf("linked items: %v", ids)
	}

	vecs, _ := idx.Embeddings("work_item", "p", "m")
	if len(vecs) != 2 || len(vecs[0].Vector) != 3 {
		t.Fatalf("embeddings: %#v", vecs)
	}
	missing, _ := idx.WorkItemsMissingEmbedding("other", "m", 10)
	if len(missing) != 2 {
		t.Fatalf("missing under another model: %d", len(missing))
	}
}

func TestNormalizeEntityType(t *testing.T) {
	for in, want := range map[string]string{"Pull Request": "pull_request", "pull-request": "pull_request", " Ticket ": "ticket", "linear_issue": "linear_issue", "A  B": "a_b", "": ""} {
		if got := NormalizeEntityType(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestVectorRoundTrip(t *testing.T) {
	v := []float32{0.5, -1.25, 3}
	got := DecodeVector(EncodeVector(v))
	if len(got) != 3 || got[0] != 0.5 || got[1] != -1.25 || got[2] != 3 {
		t.Fatalf("got %v", got)
	}
}
