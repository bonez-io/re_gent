package insight

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/store"
)

func openTestIndex(t *testing.T) (*store.Store, *index.DB) {
	t.Helper()
	s, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.Open(s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return s, idx
}

func enqueue(t *testing.T, idx *index.DB, turn string) int64 {
	t.Helper()
	id, _, err := idx.EnqueueInsightJob(index.InsightJob{SessionID: "s", TurnID: turn})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWorker_NoProcessorLeavesQueueAlone(t *testing.T) {
	s, idx := openTestIndex(t)
	enqueue(t, idx, "t1")

	var lines []string
	w := &Worker{Store: s, Index: idx, Log: func(l string) { lines = append(lines, l) }}
	_, held, err := w.Run(context.Background())
	if !held || !errors.Is(err, ErrNoProcessor) {
		t.Fatalf("held=%v err=%v", held, err)
	}
	counts, _ := idx.InsightJobCounts()
	if counts[index.InsightJobQueued] != 1 {
		t.Fatalf("job must stay queued: %v", counts)
	}
	if _, alive := Holder(s.Root); alive {
		t.Fatal("lock must be released on exit")
	}
}

func TestWorker_DrainsDoneRetryAndPermanent(t *testing.T) {
	s, idx := openTestIndex(t)
	enqueue(t, idx, "ok")
	enqueue(t, idx, "flaky")
	enqueue(t, idx, "broken")

	calls := map[string]int{}
	w := &Worker{Store: s, Index: idx, Log: func(string) {}, Processor: ProcessorFunc(func(_ context.Context, job index.InsightJob) error {
		calls[job.TurnID]++
		switch job.TurnID {
		case "flaky":
			if calls["flaky"] < 2 {
				return errors.New("transient")
			}
			return nil
		case "broken":
			return Permanent(errors.New("reply did not parse"))
		}
		return nil
	})}

	report, held, err := w.Run(context.Background())
	if err != nil || !held {
		t.Fatalf("run: held=%v err=%v", held, err)
	}
	if report.Done != 2 || report.Retried != 1 || report.Failed != 1 {
		t.Fatalf("report: %#v", report)
	}
	if calls["flaky"] != 2 || calls["broken"] != 1 {
		t.Fatalf("calls: %v", calls)
	}
	counts, _ := idx.InsightJobCounts()
	if counts[index.InsightJobDone] != 2 || counts[index.InsightJobFailed] != 1 || counts[index.InsightJobQueued] != 0 {
		t.Fatalf("counts: %v", counts)
	}
	failed, _, _ := idx.LastFailedInsightJob()
	if !strings.Contains(failed.LastError, "did not parse") {
		t.Fatalf("permanent error should be recorded: %#v", failed)
	}
}

func TestWorker_GivesUpAfterMaxAttempts(t *testing.T) {
	s, idx := openTestIndex(t)
	enqueue(t, idx, "always")
	attempts := 0
	w := &Worker{Store: s, Index: idx, Log: func(string) {}, Processor: ProcessorFunc(func(context.Context, index.InsightJob) error {
		attempts++
		return errors.New("nope")
	})}
	report, _, err := w.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if attempts != MaxAttempts || report.Retried != MaxAttempts-1 || report.Failed != 1 {
		t.Fatalf("attempts=%d report=%#v", attempts, report)
	}
}

func TestWorker_SecondWorkerYields(t *testing.T) {
	s, idx := openTestIndex(t)
	lock, _, err := TryLock(s.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	w := &Worker{Store: s, Index: idx, Log: func(string) {}, Processor: ProcessorFunc(func(context.Context, index.InsightJob) error { return nil })}
	_, held, err := w.Run(context.Background())
	if held || err != nil {
		t.Fatalf("held=%v err=%v", held, err)
	}
}

func TestWorker_ResetsJobsLeftRunning(t *testing.T) {
	s, idx := openTestIndex(t)
	enqueue(t, idx, "t1")
	if _, _, err := idx.ClaimInsightJob(); err != nil { // simulate a dead worker
		t.Fatal(err)
	}
	w := &Worker{Store: s, Index: idx, Log: func(string) {}, Processor: ProcessorFunc(func(context.Context, index.InsightJob) error { return nil })}
	report, _, err := w.Run(context.Background())
	if err != nil || report.Reset != 1 || report.Done != 1 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestWorker_StopsOnCancel(t *testing.T) {
	s, idx := openTestIndex(t)
	enqueue(t, idx, "t1")
	enqueue(t, idx, "t2")
	ctx, cancel := context.WithCancel(context.Background())
	w := &Worker{Store: s, Index: idx, Log: func(string) {}, Processor: ProcessorFunc(func(context.Context, index.InsightJob) error {
		cancel()
		return nil
	})}
	report, _, err := w.Run(ctx)
	if !errors.Is(err, context.Canceled) || report.Done != 1 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	counts, _ := idx.InsightJobCounts()
	if counts[index.InsightJobQueued] != 1 {
		t.Fatalf("second job should still be queued: %v", counts)
	}
}
