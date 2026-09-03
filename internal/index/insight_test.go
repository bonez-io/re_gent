package index

import (
	"errors"
	"testing"

	"github.com/bonez-io/re_gent/internal/store"
)

func openTestIndex(t *testing.T) (*store.Store, *DB) {
	t.Helper()
	s, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	idx, err := Open(s)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return s, idx
}

func TestInsightSchema_OpenTwiceIsIdempotent(t *testing.T) {
	s, idx := openTestIndex(t)
	if err := idx.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	again, err := Open(s)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	for _, table := range []string{"work_items", "work_item_steps", "entities", "work_item_entities", "embeddings", "insight_jobs", "insight_meta", "messages_fts", "work_items_fts", "entities_fts"} {
		var n int
		if err := again.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s: present=%d err=%v", table, n, err)
		}
	}
}

func TestEnqueueInsightJob_DedupesQueuedTurn(t *testing.T) {
	_, idx := openTestIndex(t)
	job := InsightJob{SessionID: "s1", TurnID: "t1", StepID: "abc"}

	id1, inserted, err := idx.EnqueueInsightJob(job)
	if err != nil || !inserted {
		t.Fatalf("first enqueue: inserted=%v err=%v", inserted, err)
	}
	id2, inserted, err := idx.EnqueueInsightJob(job)
	if err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
	if inserted || id2 != id1 {
		t.Fatalf("second enqueue should return the queued job: inserted=%v id=%d want %d", inserted, id2, id1)
	}

	// A different turn in the same session is a new job.
	_, inserted, err = idx.EnqueueInsightJob(InsightJob{SessionID: "s1", TurnID: "t2"})
	if err != nil || !inserted {
		t.Fatalf("different turn: inserted=%v err=%v", inserted, err)
	}

	// Once the first job is done, the same turn may be queued again.
	if err := idx.CompleteInsightJob(id1); err != nil {
		t.Fatalf("complete: %v", err)
	}
	_, inserted, err = idx.EnqueueInsightJob(job)
	if err != nil || !inserted {
		t.Fatalf("re-enqueue after done: inserted=%v err=%v", inserted, err)
	}

	if _, _, err := idx.EnqueueInsightJob(InsightJob{}); err == nil {
		t.Fatal("empty session id should be rejected")
	}
}

func TestClaimInsightJob_OldestFirstAndCountsAttempts(t *testing.T) {
	_, idx := openTestIndex(t)
	if _, _, err := idx.EnqueueInsightJob(InsightJob{SessionID: "s1", TurnID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := idx.EnqueueInsightJob(InsightJob{SessionID: "s1", TurnID: "t2"}); err != nil {
		t.Fatal(err)
	}

	job, ok, err := idx.ClaimInsightJob()
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if job.TurnID != "t1" || job.State != InsightJobRunning || job.Attempts != 1 {
		t.Fatalf("unexpected claimed job: %#v", job)
	}

	// Retry returns it to the queue behind t2, keeping the attempt count.
	if err := idx.FailInsightJob(job.ID, errors.New("boom"), true); err != nil {
		t.Fatal(err)
	}
	next, ok, err := idx.ClaimInsightJob()
	if err != nil || !ok {
		t.Fatalf("claim next: ok=%v err=%v", ok, err)
	}
	if next.TurnID != "t2" {
		t.Fatalf("expected t2 next (oldest queued by id), got %s", next.TurnID)
	}
	if err := idx.CompleteInsightJob(next.ID); err != nil {
		t.Fatal(err)
	}

	retried, ok, err := idx.ClaimInsightJob()
	if err != nil || !ok {
		t.Fatalf("claim retried: ok=%v err=%v", ok, err)
	}
	if retried.ID != job.ID || retried.Attempts != 2 || retried.LastError != "boom" {
		t.Fatalf("retried job should keep its id, count attempts, and carry the error: %#v", retried)
	}
	if err := idx.FailInsightJob(retried.ID, errors.New("still broken"), false); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := idx.ClaimInsightJob(); err != nil || ok {
		t.Fatalf("queue should be empty: ok=%v err=%v", ok, err)
	}
	counts, err := idx.InsightJobCounts()
	if err != nil {
		t.Fatal(err)
	}
	if counts[InsightJobDone] != 1 || counts[InsightJobFailed] != 1 || counts[InsightJobQueued] != 0 {
		t.Fatalf("unexpected counts: %v", counts)
	}
	failed, ok, err := idx.LastFailedInsightJob()
	if err != nil || !ok || failed.LastError != "still broken" {
		t.Fatalf("last failed: ok=%v err=%v job=%#v", ok, err, failed)
	}
}

func TestResetRunningInsightJobs(t *testing.T) {
	_, idx := openTestIndex(t)
	if _, _, err := idx.EnqueueInsightJob(InsightJob{SessionID: "s1", TurnID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := idx.ClaimInsightJob(); err != nil {
		t.Fatal(err)
	}
	n, err := idx.ResetRunningInsightJobs()
	if err != nil || n != 1 {
		t.Fatalf("reset: n=%d err=%v", n, err)
	}
	job, ok, err := idx.ClaimInsightJob()
	if err != nil || !ok || job.Attempts != 2 {
		t.Fatalf("reset job should be claimable again with attempts counted: ok=%v err=%v job=%#v", ok, err, job)
	}
}

func TestEnqueueSessionInsightJobs_ReplacesQueue(t *testing.T) {
	_, idx := openTestIndex(t)
	for _, id := range []string{"a", "b"} {
		if err := idx.UpsertSession(SessionUpdate{ID: id, Origin: "claude_code"}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	if _, _, err := idx.EnqueueInsightJob(InsightJob{SessionID: "a", TurnID: "stale"}); err != nil {
		t.Fatal(err)
	}
	n, err := idx.EnqueueSessionInsightJobs()
	if err != nil || n != 2 {
		t.Fatalf("queued %d err=%v, want 2", n, err)
	}
	jobs, err := idx.ListInsightJobs(InsightJobQueued, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("stale turn job should be gone; queued=%d", len(jobs))
	}
	for _, job := range jobs {
		if job.Kind != InsightJobKindSession {
			t.Fatalf("expected session jobs, got %#v", job)
		}
	}
}

func TestMessagesFTS_TriggersAndRebuild(t *testing.T) {
	_, idx := openTestIndex(t)
	if err := idx.AppendMessage(Message{ID: "m1", SessionID: "s1", Timestamp: 1, MessageType: "user", ContentText: "fix the flaky retry loop"}); err != nil {
		t.Fatal(err)
	}
	if err := idx.AppendMessage(Message{ID: "m2", SessionID: "s1", Timestamp: 2, MessageType: "assistant", ContentText: "done, see queue.go"}); err != nil {
		t.Fatal(err)
	}

	var hits int
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'flaky'`).Scan(&hits); err != nil {
		t.Fatalf("match: %v", err)
	}
	if hits != 1 {
		t.Fatalf("trigger should index new messages; hits=%d", hits)
	}

	cov, err := idx.InsightCoverage()
	if err != nil {
		t.Fatal(err)
	}
	if cov.Messages != 2 || cov.MessagesIndexed != 2 {
		t.Fatalf("coverage: %#v", cov)
	}

	if err := idx.RebuildInsightFTS(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'queue'`).Scan(&hits); err != nil || hits != 1 {
		t.Fatalf("after rebuild: hits=%d err=%v", hits, err)
	}
	if at, err := idx.InsightMeta("fts_rebuilt_at"); err != nil || at == "" {
		t.Fatalf("rebuild should stamp insight_meta: %q %v", at, err)
	}
}

func TestInsightMeta_RoundTrip(t *testing.T) {
	_, idx := openTestIndex(t)
	if v, err := idx.InsightMeta("missing"); err != nil || v != "" {
		t.Fatalf("missing key: %q %v", v, err)
	}
	if err := idx.SetInsightMeta("k", "1"); err != nil {
		t.Fatal(err)
	}
	if err := idx.SetInsightMeta("k", "2"); err != nil {
		t.Fatal(err)
	}
	if v, err := idx.InsightMeta("k"); err != nil || v != "2" {
		t.Fatalf("got %q %v", v, err)
	}
}
