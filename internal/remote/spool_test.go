package remote

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
)

func newSpool(t *testing.T) *Spool {
	t.Helper()
	s, err := OpenSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatalf("OpenSpool: %v", err)
	}
	return s
}

func hashOf(s string) store.Hash { return store.HashBytes([]byte(s)) }

func TestSpoolHighWaterMark(t *testing.T) {
	s := newSpool(t)

	// An absent mark must read as "the server has nothing": the safe direction,
	// because it causes a redundant push rather than a skipped one.
	got, err := s.PushedTip(testRef)
	if err != nil || got != "" {
		t.Fatalf("PushedTip on empty spool = %q, %v", got, err)
	}

	tip := hashOf("step-1")
	if err := s.RecordPushed(testRef, tip); err != nil {
		t.Fatalf("RecordPushed: %v", err)
	}
	if got, _ := s.PushedTip(testRef); got != tip {
		t.Fatalf("PushedTip = %s, want %s", got, tip)
	}

	if err := s.ForgetPushed(testRef); err != nil {
		t.Fatalf("ForgetPushed: %v", err)
	}
	if got, _ := s.PushedTip(testRef); got != "" {
		t.Fatalf("PushedTip after forget = %s, want empty", got)
	}
	// Forgetting twice must not be an error; the outbox is replay-safe.
	if err := s.ForgetPushed(testRef); err != nil {
		t.Fatalf("second ForgetPushed: %v", err)
	}
}

func TestSpoolRejectsBadMarks(t *testing.T) {
	s := newSpool(t)

	if err := s.RecordPushed("sessions/../escape", hashOf("x")); err == nil {
		t.Error("a traversal ref name must be rejected")
	}
	if err := s.RecordPushed(testRef, "not-a-hash"); err == nil {
		t.Error("a malformed hash must be rejected")
	}
	if err := s.MarkObject("nope"); err == nil {
		t.Error("a malformed object hash must be rejected")
	}
}

func TestSpoolCorruptMarkFallsBackToFullPush(t *testing.T) {
	s := newSpool(t)
	if err := os.WriteFile(s.refMarkPath(testRef), []byte("garbage"), 0o644); err != nil {
		t.Fatalf("write corrupt mark: %v", err)
	}

	got, err := s.PushedTip(testRef)
	if err != nil {
		t.Fatalf("PushedTip: %v", err)
	}
	if got != "" {
		t.Fatalf("corrupt mark returned %q; it must be treated as unknown", got)
	}
}

func TestSpoolLooseObjectQueue(t *testing.T) {
	s := newSpool(t)
	a, b := hashOf("a"), hashOf("b")

	for _, h := range []store.Hash{a, b, a} { // duplicate marks must be idempotent
		if err := s.MarkObject(h); err != nil {
			t.Fatalf("MarkObject: %v", err)
		}
	}

	pending, err := s.PendingObjects()
	if err != nil {
		t.Fatalf("PendingObjects: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %v, want 2 unique objects", pending)
	}

	if err := s.ClearObject(a); err != nil {
		t.Fatalf("ClearObject: %v", err)
	}
	if err := s.ClearObject(a); err != nil {
		t.Fatalf("clearing twice must be safe: %v", err)
	}
	pending, _ = s.PendingObjects()
	if len(pending) != 1 || pending[0] != b {
		t.Fatalf("pending after clear = %v, want [%s]", pending, b)
	}
}

func TestSpoolIsBounded(t *testing.T) {
	s := newSpool(t)

	// Fill the queue directly; going through MarkObject would be O(n^2).
	for i := 0; i < maxSpooledObjects; i++ {
		name := filepath.Join(s.dir, "objects", string(hashOf(fmt.Sprintf("pad-%d", i))))
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatalf("seed queue: %v", err)
		}
	}

	// Overflow must be reported, never silently dropped: it means captured
	// bytes are not going to be delivered.
	err := s.MarkObject(hashOf("one-too-many"))
	if !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("MarkObject at capacity = %v, want ErrSpoolFull", err)
	}
	if !strings.Contains(err.Error(), "rgt sync") {
		t.Errorf("the overflow error must tell the user how to recover, got %q", err)
	}
}

func TestSpoolCooldown(t *testing.T) {
	s := newSpool(t)
	now := time.Unix(1_700_000_000, 0)

	cooling, _, err := s.InCooldown(now)
	if err != nil || cooling {
		t.Fatalf("fresh spool = cooling %v, %v; want not cooling", cooling, err)
	}

	if err := s.StartCooldown(now.Add(30 * time.Second)); err != nil {
		t.Fatalf("StartCooldown: %v", err)
	}
	cooling, until, err := s.InCooldown(now)
	if err != nil || !cooling {
		t.Fatalf("InCooldown during window = %v, %v", cooling, err)
	}
	if !until.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("cooldown until = %v, want %v", until, now.Add(30*time.Second))
	}

	// Once the window passes, retries resume without any explicit clear.
	if cooling, _, _ = s.InCooldown(now.Add(31 * time.Second)); cooling {
		t.Fatal("cooldown must expire on its own")
	}

	if err := s.ClearCooldown(); err != nil {
		t.Fatalf("ClearCooldown: %v", err)
	}
	if cooling, _, _ = s.InCooldown(now); cooling {
		t.Fatal("cooldown must be cleared after a successful sync")
	}
}

func TestSpoolCooldownFailsOpen(t *testing.T) {
	s := newSpool(t)
	if err := os.WriteFile(filepath.Join(s.dir, cooldownFile), []byte("not-a-number"), 0o644); err != nil {
		t.Fatalf("write corrupt cooldown: %v", err)
	}

	// A corrupt marker must not suppress delivery; attempting is the safe
	// direction.
	cooling, _, err := s.InCooldown(time.Now())
	if cooling {
		t.Fatal("a corrupt cooldown marker must not suppress retries")
	}
	if err == nil {
		t.Fatal("a corrupt cooldown marker should be reported")
	}
}

func TestSpoolKnownRefs(t *testing.T) {
	s := newSpool(t)
	if err := s.RecordPushed("sessions/claude_code--a", hashOf("a")); err != nil {
		t.Fatalf("RecordPushed: %v", err)
	}
	if err := s.RecordPushed("sessions/codex_cli--b", hashOf("b")); err != nil {
		t.Fatalf("RecordPushed: %v", err)
	}

	refs, err := s.KnownRefs()
	if err != nil {
		t.Fatalf("KnownRefs: %v", err)
	}
	want := []string{"sessions/claude_code--a", "sessions/codex_cli--b"}
	if len(refs) != len(want) {
		t.Fatalf("KnownRefs = %v, want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("KnownRefs = %v, want %v", refs, want)
		}
	}
}

func TestSpoolStatusIsComputedOffline(t *testing.T) {
	f := newFixture(t)
	f.addStep(t, map[string]string{"a.txt": "one"}, "first")
	f.addStep(t, map[string]string{"a.txt": "two"}, "second")

	status, err := f.spool.Status(f.cache)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Clean() {
		t.Fatal("two unpushed steps must not read as clean")
	}
	if status.PendingRefs != 1 || status.PendingSteps != 2 {
		t.Fatalf("status = %+v, want 1 ref and 2 steps pending", status)
	}

	// After the mark advances, the same computation reports nothing owed —
	// still without touching the network.
	tip, err := f.cache.ReadRef(testRef)
	if err != nil {
		t.Fatalf("read ref: %v", err)
	}
	if err := f.spool.RecordPushed(testRef, tip); err != nil {
		t.Fatalf("RecordPushed: %v", err)
	}
	status, err = f.spool.Status(f.cache)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Clean() {
		t.Fatalf("status = %+v, want clean", status)
	}
}
