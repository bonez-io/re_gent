package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/regent-vcs/regent/internal/capture"

	"github.com/regent-vcs/regent/internal/index"
	"github.com/regent-vcs/regent/internal/store"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// The fixture is a real package.json, not a two-line toy. The mismatched
// encode/decode pair in the old LineDiff only misattributed when the diff had
// enough surrounding context to shift: every unit test in internal/diff passed
// while the bug was live, and it took a file this shape to expose it.
const (
	pkgV1 = `{
  "name": "job-hunter",
  "version": "0.1.0",
  "description": "AI-powered job hunting agent",
  "main": "dist/index.js",
  "bin": {
    "job-hunter": "dist/index.js"
  },
  "scripts": {
    "dev": "tsx src/index.ts",
    "build": "tsc",
    "start": "tsx src/index.ts",
    "typecheck": "tsc --noEmit",
    "test": "node --import tsx --test src/**/*.test.ts"
  },
  "dependencies": {
    "openai": "^4.67.0",
    "better-sqlite3": "^9.6.0",
    "chalk": "^4.1.2",
    "commander": "^12.1.0",
    "dotenv": "^16.4.5",
    "inquirer": "^8.2.6",
    "mammoth": "^1.8.0",
    "ora": "^5.4.1",
    "pdf-parse": "^1.1.1",
    "zod": "^3.23.8"
  },
  "devDependencies": {
    "@types/better-sqlite3": "^7.6.11",
    "@types/inquirer": "^8.2.10",
    "@types/node": "^22.7.0",
    "@types/pdf-parse": "^1.1.4",
    "tsx": "^4.19.1",
    "typescript": "^5.6.2"
  }
}
`
	pkgV2 = `{
  "name": "job-hunter",
  "version": "0.1.1",
  "description": "AI-powered job hunting agent",
  "main": "dist/index.js",
  "bin": {
    "job-hunter": "dist/index.js"
  },
  "scripts": {
    "dev": "tsx src/index.ts",
    "build": "tsc",
    "start": "tsx src/index.ts",
    "typecheck": "tsc --noEmit",
    "test": "node --import tsx --test src/**/*.test.ts"
  },
  "dependencies": {
    "openai": "^4.67.0",
    "better-sqlite3": "^9.6.0",
    "chalk": "^4.1.2",
    "commander": "^12.1.0",
    "dotenv": "^16.4.5",
    "inquirer": "^8.2.6",
    "mammoth": "^1.8.0",
    "ora": "^5.4.1",
    "pdf-parse": "^1.1.1",
    "zod": "^3.23.8"
  },
  "devDependencies": {
    "@types/better-sqlite3": "^7.6.11",
    "@types/inquirer": "^8.2.10",
    "@types/node": "^22.7.0",
    "@types/pdf-parse": "^1.1.4",
    "tsx": "^4.19.1",
    "typescript": "^5.6.2"
  }
}
`
	pkgV3 = `{
  "name": "job-hunter",
  "version": "0.1.1",
  "description": "AI-powered job hunting agent",
  "main": "dist/index.js",
  "bin": {
    "job-hunter": "dist/index.js"
  },
  "scripts": {
    "dev": "tsx src/index.ts",
    "build": "tsc",
    "start": "tsx src/index.ts",
    "typecheck": "tsc --noEmit",
    "test": "node --import tsx --test src/**/*.test.ts"
  },
  "dependencies": {
    "openai": "^4.67.0",
    "better-sqlite3": "^9.6.0",
    "chalk": "^5.3.0",
    "commander": "^12.1.0",
    "dotenv": "^16.4.5",
    "inquirer": "^8.2.6",
    "mammoth": "^1.8.0",
    "ora": "^5.4.1",
    "pdf-parse": "^1.1.1",
    "zod": "^3.23.8"
  },
  "devDependencies": {
    "@types/better-sqlite3": "^7.6.11",
    "@types/inquirer": "^8.2.10",
    "@types/node": "^22.7.0",
    "@types/pdf-parse": "^1.1.4",
    "tsx": "^4.19.1",
    "typescript": "^5.6.2"
  }
}
`
)

func TestRepairBlameCmdFixesMapsWrittenByTheBrokenDiff(t *testing.T) {
	root := t.TempDir()
	withWorkingDir(t, root)

	s, idx := openRepairFixtureStore(t, root)

	// A three-step history of one real file: create it, bump "version",
	// then bump the "chalk" dependency.
	step1 := writeStepWithBrokenBlame(t, s, idx, "claude_code:sess-repair", "", "package.json", pkgV1, 1)
	step2 := writeStepWithBrokenBlame(t, s, idx, "claude_code:sess-repair", step1, "package.json", pkgV2, 2)
	step3 := writeStepWithBrokenBlame(t, s, idx, "claude_code:sess-repair", step2, "package.json", pkgV3, 3)
	closeRepairFixture(t, idx)

	// Guard the fixture: if the old algorithm did not actually misattribute
	// here, the rest of this test proves nothing.
	broken, err := s.ReadBlameForFile(step3, "package.json")
	if err != nil {
		t.Fatalf("read broken blame: %v", err)
	}
	if broken.Lines[blameLineOf(t, pkgV3, `"version"`)] == step2 {
		t.Fatal("fixture is not broken: the old algorithm already attributed the version line correctly")
	}

	runRepairBlame(t)

	out := runBlameCmd(t, "package.json")

	// What a person reading the diffs by hand would say.
	assertBlamedBy(t, out, `"version": "0.1.1"`, step2, "the version bump")
	assertBlamedBy(t, out, `"chalk": "^5.3.0"`, step3, "the chalk bump")
	assertBlamedBy(t, out, `"description"`, step1, "a line nobody touched after it was created")
}

func TestRepairBlameIsIdempotentAndSaysWhenNothingNeededRepair(t *testing.T) {
	root := t.TempDir()
	withWorkingDir(t, root)

	s, idx := openRepairFixtureStore(t, root)
	step1 := writeStepWithBrokenBlame(t, s, idx, "claude_code:sess-idem", "", "package.json", pkgV1, 1)
	writeStepWithBrokenBlame(t, s, idx, "claude_code:sess-idem", step1, "package.json", pkgV2, 2)
	closeRepairFixture(t, idx)

	// The root step's map was already right — a file created from nothing is
	// attributed to the step that created it whichever diff you use — so only
	// the second step's map is wrong. Both counts are real.
	if out := runRepairBlame(t); !strings.Contains(out, "1 rewritten, 1 already correct") {
		t.Fatalf("first run did not repair the broken map: %s", out)
	}

	before := snapshotDir(t, filepath.Join(root, ".regent"))

	out := runRepairBlame(t)
	if !strings.Contains(out, "Nothing to repair") {
		t.Errorf("a run that repairs nothing must say so, said: %s", out)
	}
	if !strings.Contains(out, "2 blame maps across 2 steps in 1 session") {
		t.Errorf("report does not say what it checked: %s", out)
	}

	after := snapshotDir(t, filepath.Join(root, ".regent"))
	if diff := describeStoreDiff(before, after); diff != "" {
		t.Errorf("second run changed the store:\n%s", diff)
	}
}

func TestRepairBlameInterruptedLeavesEveryMapReadableAndIsRerunnable(t *testing.T) {
	root := t.TempDir()
	withWorkingDir(t, root)

	s, idx := openRepairFixtureStore(t, root)
	step1 := writeStepWithBrokenBlame(t, s, idx, "claude_code:sess-int", "", "package.json", pkgV1, 1)
	step2 := writeStepWithBrokenBlame(t, s, idx, "claude_code:sess-int", step1, "package.json", pkgV2, 2)
	step3 := writeStepWithBrokenBlame(t, s, idx, "claude_code:sess-int", step2, "package.json", pkgV3, 3)
	closeRepairFixture(t, idx)

	// Interrupt the moment the first map has been handled: the store is now
	// part repaired, part written by the old algorithm — the state a Ctrl-C
	// leaves behind.
	ctx, cancel := context.WithCancel(context.Background())
	repair := &capture.BlameRepair{
		Store:    s,
		Progress: func(store.Hash, string, bool) { cancel() },
	}
	report, err := repair.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted run error = %v, want context.Canceled", err)
	}
	if report.Steps != 1 || report.Checked() != 1 {
		t.Fatalf("interrupted run handled %d steps / %d maps, want 1 each (it should stop, not finish)", report.Steps, report.Checked())
	}

	// Every map still on disk decodes, and blame still answers.
	for _, stepHash := range []store.Hash{step1, step2, step3} {
		if _, err := s.ReadBlameForFile(stepHash, "package.json"); err != nil {
			t.Fatalf("blame map for step %s is unreadable after an interrupted run: %v", stepHash[:8], err)
		}
	}
	runBlameCmd(t, "package.json")

	// Re-running from scratch finishes the job.
	runRepairBlame(t)
	assertBlamedBy(t, runBlameCmd(t, "package.json"), `"version": "0.1.1"`, step2, "the version bump")
}

func TestRepairBlameWalksEverySessionRefAndCountsSharedStepsOnce(t *testing.T) {
	root := t.TempDir()
	withWorkingDir(t, root)

	s, idx := openRepairFixtureStore(t, root)
	// Two sessions sharing a root: the DAG dedupes common history, so the
	// shared step must be recomputed once and both tips must be repaired.
	shared := writeStepWithBrokenBlame(t, s, idx, "claude_code:sess-a", "", "package.json", pkgV1, 1)
	tipA := writeStepWithBrokenBlame(t, s, idx, "claude_code:sess-a", shared, "package.json", pkgV2, 2)
	if err := s.UpdateRef("sessions/claude_code:sess-b", "", shared); err != nil {
		t.Fatalf("seed second session ref: %v", err)
	}
	tipB := writeStepWithBrokenBlame(t, s, idx, "claude_code:sess-b", shared, "package.json", pkgV3, 3)
	closeRepairFixture(t, idx)

	report, err := (&capture.BlameRepair{Store: s}).Run(context.Background())
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if report.Sessions != 2 {
		t.Errorf("walked %d session refs, want 2", report.Sessions)
	}
	if report.Steps != 3 {
		t.Errorf("recomputed %d steps, want 3 (the shared root counts once)", report.Steps)
	}

	// Both tips are repaired, not just the first ref walked.
	for _, tip := range []store.Hash{tipA, tipB} {
		blame, err := s.ReadBlameForFile(tip, "package.json")
		if err != nil {
			t.Fatalf("read repaired blame: %v", err)
		}
		versionLine := blameLineOf(t, pkgV2, `"version"`)
		if blame.Lines[versionLine] != tip {
			t.Errorf("tip %s: version line attributed to %s, want the step that changed it (%s)", tip[:8], blame.Lines[versionLine][:8], tip[:8])
		}
	}
}

// snapshotDir reads every file under dir into a path -> bytes map, so a later
// snapshot can be compared byte for byte.
func snapshotDir(t *testing.T, dir string) map[string][]byte {
	t.Helper()

	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[rel] = data
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return files
}

func describeStoreDiff(before, after map[string][]byte) string {
	var complaints []string
	for path, data := range before {
		switch other, ok := after[path]; {
		case !ok:
			complaints = append(complaints, "removed "+path)
		case !bytes.Equal(data, other):
			complaints = append(complaints, "changed "+path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			complaints = append(complaints, "added "+path)
		}
	}
	sort.Strings(complaints)
	return strings.Join(complaints, "\n")
}

// ---- helpers ----

func openRepairFixtureStore(t *testing.T, root string) (*store.Store, *index.DB) {
	t.Helper()

	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	idx, err := index.Open(s)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	return s, idx
}

func closeRepairFixture(t *testing.T, idx *index.DB) {
	t.Helper()
	if err := idx.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}
}

// writeStepWithBrokenBlame records one step and writes the blame sidecar the
// *old*, broken diff would have written — the state a store recorded before the
// LineDiff fix is in.
func writeStepWithBrokenBlame(t *testing.T, s *store.Store, idx *index.DB, sessionID string, parent store.Hash, path, content string, ts int64) store.Hash {
	t.Helper()

	blobHash, err := s.WriteBlob([]byte(content))
	if err != nil {
		t.Fatalf("write blob: %v", err)
	}
	tree := &store.Tree{Entries: []store.TreeEntry{{Path: path, Blob: blobHash, Mode: 0o644}}}
	treeHash, err := s.WriteTree(tree)
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}
	step := &store.Step{
		Parent:         parent,
		Tree:           treeHash,
		SessionID:      sessionID,
		Origin:         "claude_code",
		TurnID:         string(rune('a' + ts)),
		TimestampNanos: ts * 1_000_000_000,
		Cause:          store.Cause{ToolName: "Edit", ToolUseID: "tool-" + string(rune('a'+ts))},
	}
	stepHash, err := s.WriteStep(step)
	if err != nil {
		t.Fatalf("write step: %v", err)
	}
	if err := idx.IndexStep(stepHash, step, tree); err != nil {
		t.Fatalf("index step: %v", err)
	}

	var oldContent []byte
	var oldBlame *store.BlameMap
	if parent != "" {
		parentStep, err := s.ReadStep(parent)
		if err != nil {
			t.Fatalf("read parent step: %v", err)
		}
		parentTree, err := s.ReadTree(parentStep.Tree)
		if err != nil {
			t.Fatalf("read parent tree: %v", err)
		}
		if entry := parentTree.FindEntry(path); entry != nil {
			if oldContent, err = s.ReadBlob(entry.Blob); err != nil {
				t.Fatalf("read parent blob: %v", err)
			}
		}
		if oldBlame, err = s.ReadBlameForFile(parent, path); err != nil {
			t.Fatalf("read parent blame: %v", err)
		}
	}

	if err := s.WriteBlameForFile(stepHash, path, brokenComputeBlame(oldContent, []byte(content), oldBlame, stepHash)); err != nil {
		t.Fatalf("write broken blame: %v", err)
	}

	if err := s.UpdateRef("sessions/"+sessionID, parent, stepHash); err != nil {
		t.Fatalf("update session ref: %v", err)
	}

	return stepHash
}

func runRepairBlame(t *testing.T) string {
	t.Helper()

	cmd := RepairCmd()
	cmd.SetArgs([]string{"blame"})
	var err error
	out := captureStdout(func() { err = cmd.Execute() })
	if err != nil {
		t.Fatalf("repair blame: %v\noutput: %s", err, out)
	}
	return out
}

func runBlameCmd(t *testing.T, path string) string {
	t.Helper()

	cmd := BlameCmd()
	cmd.SetArgs([]string{path})
	var err error
	out := captureStdout(func() { err = cmd.Execute() })
	if err != nil {
		t.Fatalf("blame %s: %v", path, err)
	}
	return out
}

// assertBlamedBy finds the `rgt blame` output row carrying needle and checks it
// names want.
func assertBlamedBy(t *testing.T, blameOutput, needle string, want store.Hash, why string) {
	t.Helper()

	for _, row := range strings.Split(blameOutput, "\n") {
		if !strings.Contains(row, needle) {
			continue
		}
		if !strings.Contains(row, string(want)[:8]) {
			t.Errorf("blame for %s (%s) names the wrong step:\n%s\nwant %s", needle, why, row, want[:8])
		}
		return
	}
	t.Errorf("no blame row for %s in:\n%s", needle, blameOutput)
}

// blameLineOf returns the zero-based index of the first line of content holding
// needle.
func blameLineOf(t *testing.T, content, needle string) int {
	t.Helper()

	for i, line := range strings.Split(content, "\n") {
		if strings.Contains(line, needle) {
			return i
		}
	}
	t.Fatalf("no line containing %q", needle)
	return -1
}

// ---- the old, broken blame computation, kept alive only as a fixture ----

// brokenComputeBlame reproduces what store.ComputeBlame did before the LineDiff
// fix: DiffLinesToRunes paired with DiffCharsToLines, whose rune values do not
// index the array the other reads, then newline-counting on chunks that hold the
// wrong lines. It exists so a test can build a store that looks exactly like one
// recorded by the released, broken binary.
func brokenComputeBlame(oldContent, newContent []byte, oldBlame *store.BlameMap, currentStep store.Hash) *store.BlameMap {
	newBlame := &store.BlameMap{Lines: make([]store.Hash, 0)}

	for _, op := range brokenLineDiff(oldContent, newContent) {
		switch op.tag {
		case "equal":
			for i := op.i1; i < op.i2; i++ {
				if oldBlame != nil && i < len(oldBlame.Lines) {
					newBlame.Lines = append(newBlame.Lines, oldBlame.Lines[i])
				} else {
					newBlame.Lines = append(newBlame.Lines, currentStep)
				}
			}
		case "insert", "replace":
			for j := op.j1; j < op.j2; j++ {
				newBlame.Lines = append(newBlame.Lines, currentStep)
			}
		case "delete":
		}
	}

	return newBlame
}

type brokenOpcode struct {
	tag            string
	i1, i2, j1, j2 int
}

func brokenLineDiff(oldContent, newContent []byte) []brokenOpcode {
	dmp := diffmatchpatch.New()
	a, b, lineArray := dmp.DiffLinesToRunes(brokenJoinLines(oldContent), brokenJoinLines(newContent))
	diffs := dmp.DiffCharsToLines(dmp.DiffMainRunes(a, b, false), lineArray)

	var opcodes []brokenOpcode
	i1, i2, j1, j2 := 0, 0, 0, 0
	for _, d := range diffs {
		lineCount := strings.Count(d.Text, "\n")
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			i1, i2 = i2, i2+lineCount
			j1, j2 = j2, j2+lineCount
			opcodes = append(opcodes, brokenOpcode{"equal", i1, i2, j1, j2})
		case diffmatchpatch.DiffDelete:
			i1, i2 = i2, i2+lineCount
			opcodes = append(opcodes, brokenOpcode{"delete", i1, i2, j1, j1})
		case diffmatchpatch.DiffInsert:
			j1, j2 = j2, j2+lineCount
			opcodes = append(opcodes, brokenOpcode{"insert", i1, i1, j1, j2})
		}
	}

	// The old code merged an adjacent delete+insert into a replace.
	var merged []brokenOpcode
	for i := 0; i < len(opcodes); i++ {
		if i+1 < len(opcodes) && opcodes[i].tag == "delete" && opcodes[i+1].tag == "insert" {
			merged = append(merged, brokenOpcode{"replace", opcodes[i].i1, opcodes[i].i2, opcodes[i+1].j1, opcodes[i+1].j2})
			i++
			continue
		}
		merged = append(merged, opcodes[i])
	}
	return merged
}

func brokenJoinLines(content []byte) string {
	if len(content) == 0 {
		return ""
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
