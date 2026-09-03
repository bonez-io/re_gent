package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/store"
)

func writeWorkspaceFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestWorkspaceSync_WritesBaselineStep(t *testing.T) {
	root := t.TempDir()
	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	writeWorkspaceFile(t, root, "a.txt", "hello\n")
	writeWorkspaceFile(t, root, "nested/b.txt", "world\n")

	stepHash, wrote, fileCount, err := WorkspaceSync(s, root)
	if err != nil {
		t.Fatalf("WorkspaceSync: %v", err)
	}
	if !wrote {
		t.Fatal("expected a new sync step to be written")
	}
	if fileCount != 2 {
		t.Fatalf("expected 2 files in baseline, got %d", fileCount)
	}
	if stepHash == "" {
		t.Fatal("expected a non-empty step hash")
	}

	step, err := s.ReadStep(stepHash)
	if err != nil {
		t.Fatalf("read sync step: %v", err)
	}
	if step.Origin != SyncOrigin {
		t.Fatalf("expected origin %q, got %q", SyncOrigin, step.Origin)
	}
	if step.SessionID != WorkspaceSyncSessionID {
		t.Fatalf("expected session id %q, got %q", WorkspaceSyncSessionID, step.SessionID)
	}
	if step.Parent != "" {
		t.Fatalf("expected first sync step to have no parent, got %q", step.Parent)
	}
	if len(step.Causes) != 0 || step.Cause.ToolName != "" {
		t.Fatal("sync step must carry no causes")
	}
	if step.Conversation != "" || step.Transcript != "" {
		t.Fatal("sync step must carry no conversation or transcript")
	}

	ref, err := s.ReadRef(WorkspaceSyncRef)
	if err != nil {
		t.Fatalf("read workspace sync ref: %v", err)
	}
	if ref != stepHash {
		t.Fatalf("expected refs/%s to point at %s, got %s", WorkspaceSyncRef, stepHash, ref)
	}

	// Blame was computed for the baseline: an unmodified file's line is
	// attributed to the sync step itself.
	blame, err := s.ReadBlameForFile(stepHash, "a.txt")
	if err != nil {
		t.Fatalf("read blame for a.txt: %v", err)
	}
	if len(blame.Lines) != 1 || blame.Lines[0] != stepHash {
		t.Fatalf("expected a.txt line attributed to sync step, got %+v", blame.Lines)
	}
}

func TestWorkspaceSync_IdempotentWhenUnchanged(t *testing.T) {
	root := t.TempDir()
	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	writeWorkspaceFile(t, root, "a.txt", "hello\n")

	first, wrote, _, err := WorkspaceSync(s, root)
	if err != nil {
		t.Fatalf("first WorkspaceSync: %v", err)
	}
	if !wrote {
		t.Fatal("expected the first sync to write a step")
	}

	second, wrote, fileCount, err := WorkspaceSync(s, root)
	if err != nil {
		t.Fatalf("second WorkspaceSync: %v", err)
	}
	if wrote {
		t.Fatal("expected the second sync (no changes) to be a no-op")
	}
	if second != first {
		t.Fatalf("expected the tip to stay at %s, got %s", first, second)
	}
	if fileCount != 1 {
		t.Fatalf("expected reported file count to stay 1, got %d", fileCount)
	}
}

func TestWorkspaceSync_ChainsOnChange(t *testing.T) {
	root := t.TempDir()
	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	writeWorkspaceFile(t, root, "a.txt", "hello\n")

	first, _, _, err := WorkspaceSync(s, root)
	if err != nil {
		t.Fatalf("first WorkspaceSync: %v", err)
	}

	writeWorkspaceFile(t, root, "a.txt", "hello\nworld\n")
	second, wrote, _, err := WorkspaceSync(s, root)
	if err != nil {
		t.Fatalf("second WorkspaceSync: %v", err)
	}
	if !wrote {
		t.Fatal("expected a changed workspace to write a new sync step")
	}
	if second == first {
		t.Fatal("expected a new step hash after the change")
	}

	step, err := s.ReadStep(second)
	if err != nil {
		t.Fatalf("read second sync step: %v", err)
	}
	if step.Parent != first {
		t.Fatalf("expected second sync step to chain onto the first, got parent %q", step.Parent)
	}
}

// TestComputeBlameForStep_SeedsFromWorkspaceBaseline is the core of the
// design: a first-of-session agent step's snapshot includes every file in the
// working tree (snapshotWorkspace always captures the whole tree, not just
// what the agent touched), and without seeding from the baseline every one of
// those pre-existing lines would be misattributed to the agent's first step.
func TestComputeBlameForStep_SeedsFromWorkspaceBaseline(t *testing.T) {
	root := t.TempDir()
	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	writeWorkspaceFile(t, root, "untouched.txt", "line one\nline two\n")
	writeWorkspaceFile(t, root, "edited.txt", "before\n")

	syncHash, _, _, err := WorkspaceSync(s, root)
	if err != nil {
		t.Fatalf("WorkspaceSync: %v", err)
	}

	// Now the agent turn: untouched.txt is unchanged, edited.txt gained a line.
	writeWorkspaceFile(t, root, "edited.txt", "before\nafter\n")
	agentTreeHash, err := snapshotWorkspace(s, root)
	if err != nil {
		t.Fatalf("snapshot agent tree: %v", err)
	}
	agentStepHash, err := s.WriteStep(&store.Step{
		Tree:           agentTreeHash,
		SessionID:      "claude_code:session",
		Origin:         OriginClaudeCode,
		TimestampNanos: 2,
	})
	if err != nil {
		t.Fatalf("write agent step: %v", err)
	}

	// parentHash "" mirrors the first step of a brand-new session: no session
	// history yet, but the workspace baseline exists.
	if err := computeAndWriteBlame(s, "", agentStepHash, agentTreeHash); err != nil {
		t.Fatalf("computeAndWriteBlame: %v", err)
	}

	untouchedBlame, err := s.ReadBlameForFile(agentStepHash, "untouched.txt")
	if err != nil {
		t.Fatalf("read blame for untouched.txt: %v", err)
	}
	if len(untouchedBlame.Lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(untouchedBlame.Lines))
	}
	for i, h := range untouchedBlame.Lines {
		if h != syncHash {
			t.Fatalf("line %d: expected untouched line attributed to sync step %s, got %s", i, syncHash, h)
		}
	}

	editedBlame, err := s.ReadBlameForFile(agentStepHash, "edited.txt")
	if err != nil {
		t.Fatalf("read blame for edited.txt: %v", err)
	}
	if len(editedBlame.Lines) != 2 {
		t.Fatalf("expected 2 lines in edited.txt, got %d", len(editedBlame.Lines))
	}
	if editedBlame.Lines[0] != syncHash {
		t.Fatalf("expected unchanged first line attributed to sync step %s, got %s", syncHash, editedBlame.Lines[0])
	}
	if editedBlame.Lines[1] != agentStepHash {
		t.Fatalf("expected new second line attributed to agent step %s, got %s", agentStepHash, editedBlame.Lines[1])
	}
}

// TestComputeBlameForStep_NoSyncRefUnchanged locks in that, absent a workspace
// sync ref, behaviour is exactly what it was before baseline seeding existed:
// a first step's lines are attributed to itself.
func TestComputeBlameForStep_NoSyncRefUnchanged(t *testing.T) {
	root := t.TempDir()
	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	blobHash, err := s.WriteBlob([]byte("hello\n"))
	if err != nil {
		t.Fatalf("write blob: %v", err)
	}
	treeHash, err := s.WriteTree(&store.Tree{Entries: []store.TreeEntry{{Path: "hello.txt", Blob: blobHash}}})
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}
	stepHash, err := s.WriteStep(&store.Step{Tree: treeHash, SessionID: "claude_code:session", TimestampNanos: 1})
	if err != nil {
		t.Fatalf("write step: %v", err)
	}

	if err := computeAndWriteBlame(s, "", stepHash, treeHash); err != nil {
		t.Fatalf("computeAndWriteBlame: %v", err)
	}

	blame, err := s.ReadBlameForFile(stepHash, "hello.txt")
	if err != nil {
		t.Fatalf("read blame: %v", err)
	}
	if len(blame.Lines) != 1 || blame.Lines[0] != stepHash {
		t.Fatalf("expected line attributed to the step itself with no baseline, got %+v", blame.Lines)
	}
}

// TestWorkspaceSyncOnto_ChainsOntoHint is the point of the parent-hint
// parameter: when the caller offers a step already present in the store (as
// a server-mode caller does after fetching the server's current tip), the
// new step's Parent is that hint, not whatever the local ref happened to
// hold — here, nothing.
func TestWorkspaceSyncOnto_ChainsOntoHint(t *testing.T) {
	root := t.TempDir()
	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	// Simulate a step fetched from the server: present in the store, but not
	// pointed at by the local refs/sync/workspace ref.
	hintBlob, err := s.WriteBlob([]byte("from another machine\n"))
	if err != nil {
		t.Fatalf("write blob: %v", err)
	}
	hintTree, err := s.WriteTree(&store.Tree{Entries: []store.TreeEntry{{Path: "shared.txt", Blob: hintBlob}}})
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}
	hintStep, err := s.WriteStep(&store.Step{
		Tree: hintTree, SessionID: WorkspaceSyncSessionID, Origin: SyncOrigin, TimestampNanos: 1,
	})
	if err != nil {
		t.Fatalf("write hint step: %v", err)
	}

	writeWorkspaceFile(t, root, "a.txt", "hello\n")
	stepHash, wrote, _, err := WorkspaceSyncOnto(s, root, hintStep)
	if err != nil {
		t.Fatalf("WorkspaceSyncOnto: %v", err)
	}
	if !wrote {
		t.Fatal("expected a new step (workspace differs from the hint's tree)")
	}

	step, err := s.ReadStep(stepHash)
	if err != nil {
		t.Fatalf("read step: %v", err)
	}
	if step.Parent != hintStep {
		t.Fatalf("Parent = %s, want the hint %s", step.Parent, hintStep)
	}

	ref, err := s.ReadRef(WorkspaceSyncRef)
	if err != nil {
		t.Fatalf("read ref: %v", err)
	}
	if ref != stepHash {
		t.Fatalf("local ref = %s, want %s", ref, stepHash)
	}
}

// TestWorkspaceSyncOnto_FallsBackWhenHintMissing asserts that a hint naming a
// step this store does not actually have degrades to WorkspaceSync's
// ordinary local-tip behaviour, never an error — the fetch that should have
// populated it may simply have failed (offline, timed out).
func TestWorkspaceSyncOnto_FallsBackWhenHintMissing(t *testing.T) {
	root := t.TempDir()
	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	writeWorkspaceFile(t, root, "a.txt", "hello\n")

	missingHint := store.Hash(strings.Repeat("d", 64))
	stepHash, wrote, _, err := WorkspaceSyncOnto(s, root, missingHint)
	if err != nil {
		t.Fatalf("WorkspaceSyncOnto: %v", err)
	}
	if !wrote {
		t.Fatal("expected a new (root) step")
	}
	step, err := s.ReadStep(stepHash)
	if err != nil {
		t.Fatalf("read step: %v", err)
	}
	if step.Parent != "" {
		t.Fatalf("Parent = %s, want empty (missing hint ignored)", step.Parent)
	}
}

// TestWorkspaceSyncOnto_IdempotentAgainstHintTree asserts the idempotency
// check compares against the hinted parent's tree, not the local ref's: if
// this machine's workspace already matches what the server holds, nothing is
// written, and the returned hash is the hint itself.
func TestWorkspaceSyncOnto_IdempotentAgainstHintTree(t *testing.T) {
	root := t.TempDir()
	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	writeWorkspaceFile(t, root, "a.txt", "hello\n")

	treeHash, err := snapshotWorkspace(s, root)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	hintStep, err := s.WriteStep(&store.Step{
		Tree: treeHash, SessionID: WorkspaceSyncSessionID, Origin: SyncOrigin, TimestampNanos: 1,
	})
	if err != nil {
		t.Fatalf("write hint step: %v", err)
	}

	stepHash, wrote, _, err := WorkspaceSyncOnto(s, root, hintStep)
	if err != nil {
		t.Fatalf("WorkspaceSyncOnto: %v", err)
	}
	if wrote {
		t.Fatal("expected no-op: workspace already matches the hint's tree")
	}
	if stepHash != hintStep {
		t.Fatalf("stepHash = %s, want the hint %s", stepHash, hintStep)
	}
}

// TestWorkspaceSyncOnto_LocalCASIgnoresHint locks in the fix for a real bug
// caught in review: the local ref's compare-and-swap must use the local
// ref's own current value, never the hint. Using the hint there would either
// spuriously conflict (hint != local tip) or mask a genuine local race.
func TestWorkspaceSyncOnto_LocalCASIgnoresHint(t *testing.T) {
	root := t.TempDir()
	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	// The local machine already has its own (unrelated) sync history.
	writeWorkspaceFile(t, root, "a.txt", "local one\n")
	localTip, _, _, err := WorkspaceSync(s, root)
	if err != nil {
		t.Fatalf("seed local WorkspaceSync: %v", err)
	}

	// A hint from the server, also unrelated to the local chain.
	hintBlob, err := s.WriteBlob([]byte("server state\n"))
	if err != nil {
		t.Fatalf("write blob: %v", err)
	}
	hintTree, err := s.WriteTree(&store.Tree{Entries: []store.TreeEntry{{Path: "b.txt", Blob: hintBlob}}})
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}
	hintStep, err := s.WriteStep(&store.Step{
		Tree: hintTree, SessionID: WorkspaceSyncSessionID, Origin: SyncOrigin, TimestampNanos: 2,
	})
	if err != nil {
		t.Fatalf("write hint step: %v", err)
	}

	writeWorkspaceFile(t, root, "a.txt", "local two\n")
	newStepHash, wrote, _, err := WorkspaceSyncOnto(s, root, hintStep)
	if err != nil {
		t.Fatalf("WorkspaceSyncOnto: %v", err)
	}
	if !wrote {
		t.Fatal("expected a new step")
	}

	step, err := s.ReadStep(newStepHash)
	if err != nil {
		t.Fatalf("read step: %v", err)
	}
	if step.Parent != hintStep {
		t.Fatalf("Parent = %s, want the hint %s (not the unrelated local tip %s)", step.Parent, hintStep, localTip)
	}

	ref, err := s.ReadRef(WorkspaceSyncRef)
	if err != nil {
		t.Fatalf("read ref: %v", err)
	}
	if ref != newStepHash {
		t.Fatalf("local ref = %s, want %s — the CAS must succeed against the local tip %s, not the hint", ref, newStepHash, localTip)
	}
}
