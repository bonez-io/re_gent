package insight

import (
	"context"
	"testing"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/store"
)

func withProcessor(t *testing.T) {
	t.Helper()
	previous := newProcessor
	RegisterProcessor(func(*store.Store, *index.DB, Settings) (Processor, error) {
		return ProcessorFunc(func(context.Context, index.InsightJob) error { return nil }), nil
	})
	t.Cleanup(func() { newProcessor = previous })
}

func activeRepo(t *testing.T) (*store.Store, *index.DB) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := config.Save(&config.UserConfig{Insight: config.InsightUserConfig{
		Model: config.InsightModelConfig{Provider: ProviderCommand, Command: []string{"claude", "-p"}},
	}}); err != nil {
		t.Fatal(err)
	}
	s, idx := openTestIndex(t)
	if err := s.WriteRepoConfig(store.RepoConfig{Insight: store.InsightConfig{Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	return s, idx
}

func TestEnqueuer_InactiveDoesNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, idx := openTestIndex(t)
	spawned := 0
	e := &Enqueuer{Store: s, Index: idx, CWD: s.Root, Spawn: func(string, string, string) error { spawned++; return nil }}
	if err := e.Enqueue(Turn{SessionID: "s", TurnID: "t"}); err != nil {
		t.Fatal(err)
	}
	counts, _ := idx.InsightJobCounts()
	if len(counts) != 0 || spawned != 0 {
		t.Fatalf("inactive must not queue or spawn: counts=%v spawned=%d", counts, spawned)
	}

	// Repository enabled, user without provider: still nothing.
	if err := s.WriteRepoConfig(store.RepoConfig{Insight: store.InsightConfig{Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Enqueue(Turn{SessionID: "s", TurnID: "t"}); err != nil {
		t.Fatal(err)
	}
	counts, _ = idx.InsightJobCounts()
	if len(counts) != 0 {
		t.Fatalf("no provider must not queue: %v", counts)
	}
}

func TestEnqueuer_QueuesAndSpawnsOnce(t *testing.T) {
	withProcessor(t)
	s, idx := activeRepo(t)

	var spawns []string
	e := &Enqueuer{Store: s, Index: idx, CWD: "/work", Executable: "/bin/rgt-test", Spawn: func(exe, cwd, root string) error {
		spawns = append(spawns, exe+" "+cwd+" "+root)
		return nil
	}}
	if err := e.Enqueue(Turn{SessionID: "s", TurnID: "t1", StepID: "abc"}); err != nil {
		t.Fatal(err)
	}
	if len(spawns) != 1 || spawns[0] != "/bin/rgt-test /work "+s.Root {
		t.Fatalf("spawns: %v", spawns)
	}
	jobs, _ := idx.ListInsightJobs(index.InsightJobQueued, 10)
	if len(jobs) != 1 || jobs[0].StepID != "abc" || jobs[0].TurnID != "t1" {
		t.Fatalf("jobs: %#v", jobs)
	}

	// The same turn again (a recovered Stop) is deduped and spawns nothing.
	if err := e.Enqueue(Turn{SessionID: "s", TurnID: "t1", StepID: "abc"}); err != nil {
		t.Fatal(err)
	}
	if len(spawns) != 1 {
		t.Fatalf("duplicate turn must not spawn again: %v", spawns)
	}

	// A live worker holding the lock means a new turn queues but does not spawn.
	lock, _, err := TryLock(s.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if err := e.Enqueue(Turn{SessionID: "s", TurnID: "t2"}); err != nil {
		t.Fatal(err)
	}
	jobs, _ = idx.ListInsightJobs(index.InsightJobQueued, 10)
	if len(jobs) != 2 || len(spawns) != 1 {
		t.Fatalf("jobs=%d spawns=%v", len(jobs), spawns)
	}
}

func TestEnqueuer_NoProcessorQueuesWithoutSpawning(t *testing.T) {
	previous := newProcessor
	newProcessor = nil
	t.Cleanup(func() { newProcessor = previous })
	s, idx := activeRepo(t)

	spawned := 0
	e := &Enqueuer{Store: s, Index: idx, CWD: s.Root, Spawn: func(string, string, string) error { spawned++; return nil }}
	if err := e.Enqueue(Turn{SessionID: "s", TurnID: "t1"}); err != nil {
		t.Fatal(err)
	}
	counts, _ := idx.InsightJobCounts()
	if counts[index.InsightJobQueued] != 1 || spawned != 0 {
		t.Fatalf("counts=%v spawned=%d", counts, spawned)
	}
}

func TestEnqueuer_ConfigErrorIsReturnedNotPanicked(t *testing.T) {
	s, idx := activeRepo(t)
	if err := s.WriteRepoConfig(store.RepoConfig{Insight: store.InsightConfig{Enabled: true, WorkItemIdle: "bogus"}}); err != nil {
		t.Fatal(err)
	}
	e := &Enqueuer{Store: s, Index: idx}
	if err := e.Enqueue(Turn{SessionID: "s", TurnID: "t"}); err == nil {
		t.Fatal("expected a settings error for the hook to log")
	}
	if err := (*Enqueuer)(nil).Enqueue(Turn{}); err != nil {
		t.Fatal("nil enqueuer must be a no-op")
	}
}
