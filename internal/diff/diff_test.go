package diff

import (
	"testing"
)

func TestLineDiff_NoChanges(t *testing.T) {
	old := []byte("line1\nline2\nline3\n")
	new := []byte("line1\nline2\nline3\n")
	ops := LineDiff(old, new)

	if len(ops) != 1 {
		t.Fatalf("Expected 1 op, got %d", len(ops))
	}
	if ops[0].Tag != "equal" {
		t.Errorf("Expected 'equal' op, got %s", ops[0].Tag)
	}
	if ops[0].I2-ops[0].I1 != 3 {
		t.Errorf("Expected 3 equal lines, got %d", ops[0].I2-ops[0].I1)
	}
}

func TestLineDiff_Insert(t *testing.T) {
	old := []byte("line1\nline3\n")
	new := []byte("line1\nline2\nline3\n")
	ops := LineDiff(old, new)

	// Should have: equal(line1), insert(line2), equal(line3)
	if len(ops) != 3 {
		t.Fatalf("Expected 3 ops, got %d", len(ops))
	}

	if ops[0].Tag != "equal" {
		t.Errorf("Op 0: expected 'equal', got %s", ops[0].Tag)
	}
	if ops[1].Tag != "insert" {
		t.Errorf("Op 1: expected 'insert', got %s", ops[1].Tag)
	}
	if ops[2].Tag != "equal" {
		t.Errorf("Op 2: expected 'equal', got %s", ops[2].Tag)
	}

	// Verify insert range
	if ops[1].J2-ops[1].J1 != 1 {
		t.Errorf("Expected 1 inserted line, got %d", ops[1].J2-ops[1].J1)
	}
}

func TestLineDiff_Delete(t *testing.T) {
	old := []byte("line1\nline2\nline3\n")
	new := []byte("line1\nline3\n")
	ops := LineDiff(old, new)

	// Should have: equal(line1), delete(line2), equal(line3)
	if len(ops) != 3 {
		t.Fatalf("Expected 3 ops, got %d", len(ops))
	}

	if ops[1].Tag != "delete" {
		t.Errorf("Op 1: expected 'delete', got %s", ops[1].Tag)
	}

	// Verify delete range
	if ops[1].I2-ops[1].I1 != 1 {
		t.Errorf("Expected 1 deleted line, got %d", ops[1].I2-ops[1].I1)
	}
}

func TestLineDiff_Replace(t *testing.T) {
	old := []byte("line1\nold line\nline3\n")
	new := []byte("line1\nnew line\nline3\n")
	ops := LineDiff(old, new)

	// Should have: equal, replace, equal
	if len(ops) != 3 {
		t.Fatalf("Expected 3 ops, got %d", len(ops))
	}

	if ops[1].Tag != "replace" {
		t.Errorf("Op 1: expected 'replace', got %s", ops[1].Tag)
	}

	// Verify replace range
	if ops[1].I2-ops[1].I1 != 1 {
		t.Errorf("Expected 1 old line in replace, got %d", ops[1].I2-ops[1].I1)
	}
	if ops[1].J2-ops[1].J1 != 1 {
		t.Errorf("Expected 1 new line in replace, got %d", ops[1].J2-ops[1].J1)
	}
}

func TestLineDiff_EmptyFiles(t *testing.T) {
	old := []byte("")
	new := []byte("")
	ops := LineDiff(old, new)

	if len(ops) != 0 {
		t.Errorf("Expected 0 ops for empty files, got %d", len(ops))
	}
}

func TestLineDiff_AddToEmpty(t *testing.T) {
	old := []byte("")
	new := []byte("line1\nline2\n")
	ops := LineDiff(old, new)

	if len(ops) != 1 {
		t.Fatalf("Expected 1 op, got %d", len(ops))
	}

	if ops[0].Tag != "insert" {
		t.Errorf("Expected 'insert', got %s", ops[0].Tag)
	}

	if ops[0].J2-ops[0].J1 != 2 {
		t.Errorf("Expected 2 inserted lines, got %d", ops[0].J2-ops[0].J1)
	}
}

func TestLineDiff_DeleteAll(t *testing.T) {
	old := []byte("line1\nline2\n")
	new := []byte("")
	ops := LineDiff(old, new)

	if len(ops) != 1 {
		t.Fatalf("Expected 1 op, got %d", len(ops))
	}

	if ops[0].Tag != "delete" {
		t.Errorf("Expected 'delete', got %s", ops[0].Tag)
	}

	if ops[0].I2-ops[0].I1 != 2 {
		t.Errorf("Expected 2 deleted lines, got %d", ops[0].I2-ops[0].I1)
	}
}

func TestLineDiff_MultipleChanges(t *testing.T) {
	old := []byte("line1\nline2\nline3\nline4\n")
	new := []byte("line1\nmodified2\nline3\nline5\n")
	ops := LineDiff(old, new)

	// Should detect two separate replacements
	foundReplace := false
	for _, op := range ops {
		if op.Tag == "replace" {
			foundReplace = true
		}
	}

	if !foundReplace {
		t.Errorf("Expected at least one 'replace' operation")
	}
}

// Changing one line in the middle of a file must produce a replace covering
// exactly that line, and a new side the same length as the new file.
//
// It did not. diff-match-patch emits an edit as a delete and an insert in
// whichever order it likes, and mergeReplaces only recognised delete-then-
// insert. In the other order the pair stayed unmerged: the insert added a line
// to the new side, the delete contributed none, and every line after it shifted
// by one. Blame then credited the following line to the step that edited this
// one — wrong answers for every edited file, which is most of them.
//
// Taken from a real project: bumping "version" in a package.json blamed the
// "description" line underneath it.
func TestLineDiffReplacesInPlaceRegardlessOfEditOrder(t *testing.T) {
	old := "{\n  \"name\": \"job-hunter\",\n  \"version\": \"0.1.0\",\n  \"description\": \"agent\",\n  \"main\": \"dist/index.js\"\n}\n"
	new_ := "{\n  \"name\": \"job-hunter\",\n  \"version\": \"0.1.1\",\n  \"description\": \"agent\",\n  \"main\": \"dist/index.js\"\n}\n"

	ops := LineDiff([]byte(old), []byte(new_))

	// The new side must describe exactly as many lines as the new file has.
	newLines := 0
	for _, op := range ops {
		if op.Tag != "delete" {
			newLines += op.J2 - op.J1
		}
	}
	if newLines != 6 {
		t.Errorf("ops describe %d new lines, but the file has 6; a blame map built from this is longer than the file:\n%+v", newLines, ops)
	}

	// Line 3 (index 2) is the only one that changed.
	for _, op := range ops {
		if op.Tag == "insert" || op.Tag == "replace" {
			if op.J1 != 2 || op.J2 != 3 {
				t.Errorf("the changed region is reported as new lines [%d,%d), want [2,3):\n%+v", op.J1, op.J2, ops)
			}
		}
	}
}

// The same defect as above, with the file it was actually found on. The small
// case happens to make diff-match-patch emit delete-then-insert; this one makes
// it emit the opposite order, which is the case mergeReplaces did not handle.
func TestLineDiffReplacesInPlaceOnARealFile(t *testing.T) {
	old := `{
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
	new_ := `{
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

	ops := LineDiff([]byte(old), []byte(new_))

	newLines := 0
	for _, op := range ops {
		if op.Tag != "delete" {
			newLines += op.J2 - op.J1
		}
	}
	if newLines != 36 {
		t.Errorf("ops describe %d new lines, but the file has 36 — a blame map built from this is longer than the file", newLines)
	}
	for _, op := range ops {
		if op.Tag == "insert" || op.Tag == "replace" {
			if op.J1 != 2 || op.J2 != 3 {
				t.Errorf("changed region reported as new lines [%d,%d), want [2,3) — the \"version\" line", op.J1, op.J2)
			}
		}
	}
}
