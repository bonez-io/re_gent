package server

import (
	"net/http"

	"github.com/regent-vcs/regent/internal/diff"
	"github.com/regent-vcs/regent/internal/store"
	"github.com/regent-vcs/regent/internal/treediff"
)

// diffContextLines is the number of unchanged lines shown around each change,
// matching conventional unified-diff output.
const diffContextLines = 3

// maxDiffLinesPerFile bounds how many diff lines (context + add + delete) a
// single file emits. A captured step can touch a huge generated file, and the
// endpoint must never stream an unbounded payload for one path.
const maxDiffLinesPerFile = 2000

// maxResponseDiffLines bounds the sum of diff lines across every file in one
// response, independent of the per-file cap: a step that touches many
// moderately sized files could otherwise still produce an unbounded payload.
const maxResponseDiffLines = 20000

// diffLineJSON is one line inside a hunk.
type diffLineJSON struct {
	Kind      string `json:"kind"` // "context", "add", or "delete"
	OldNumber int    `json:"old_number,omitempty"`
	NewNumber int    `json:"new_number,omitempty"`
	Content   string `json:"content"`
}

// diffHunkJSON is one contiguous block of changed (plus context) lines.
type diffHunkJSON struct {
	OldStart int            `json:"old_start"`
	OldLines int            `json:"old_lines"`
	NewStart int            `json:"new_start"`
	NewLines int            `json:"new_lines"`
	Lines    []diffLineJSON `json:"lines"`
}

// diffFileJSON is one file's diff within a GET /api/diff response.
type diffFileJSON struct {
	Path      string         `json:"path"`
	Status    string         `json:"status"` // "added", "modified", or "deleted"
	IsBinary  bool           `json:"is_binary"`
	Truncated bool           `json:"truncated"`
	Additions int            `json:"additions"`
	Deletions int            `json:"deletions"`
	Hunks     []diffHunkJSON `json:"hunks"`
}

// diffResponse is the GET /api/diff envelope.
type diffResponse struct {
	StepHash   string         `json:"step_hash"`
	ParentHash string         `json:"parent_hash"`
	TotalFiles int            `json:"total_files"`
	Files      []diffFileJSON `json:"files"`
}

// handleAPIDiff computes the per-file diff a step introduced relative to its
// parent step (tree comparison plus line-level hunks) and emits it in the
// shape the web viewer renders directly.
func (s *Server) handleAPIDiff(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store) {
	rawHash := r.URL.Query().Get("step")
	if rawHash == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing step parameter"})
		return
	}
	if !hashRE.MatchString(rawHash) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid step hash"})
		return
	}
	hash := store.Hash(rawHash)

	// ReadBlob's not-found error is not distinguishable from other read
	// failures via errors.Is, so existence is checked directly first.
	if !st.ObjectExists(hash) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "step not found"})
		return
	}
	step, err := st.ReadStep(hash)
	if err != nil {
		s.logf("read step %s in %s: %v", hash, repoID, err)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "object is not a readable step"})
		return
	}
	if step.Tree == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "object is not a readable step"})
		return
	}

	diffs, err := treediff.CompareTreesForDiff(st, step.Parent, hash)
	if err != nil {
		s.logf("compare trees for step %s in %s: %v", hash, repoID, err)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "step tree is unavailable"})
		return
	}

	resp := diffResponse{
		StepHash:   string(hash),
		ParentHash: string(step.Parent),
		Files:      s.buildFileDiffs(st, repoID, diffs),
	}
	resp.TotalFiles = len(resp.Files)
	writeJSON(w, http.StatusOK, resp)
}

// buildFileDiffs converts treediff's per-file summary into full line-level
// diffs, honoring both the per-file and whole-response line budgets. Every
// file from diffs is always reported (status, additions, deletions survive
// truncation); only the hunks content is capped, with truncated reporting the
// cut honestly instead of the file silently disappearing.
func (s *Server) buildFileDiffs(st *store.Store, repoID string, diffs []treediff.FileDiff) []diffFileJSON {
	files := make([]diffFileJSON, 0, len(diffs))
	responseBudget := maxResponseDiffLines

	for _, fd := range diffs {
		file := diffFileJSON{
			Path:      fd.Path,
			Status:    fd.Status,
			IsBinary:  fd.IsBinary,
			Additions: fd.Additions,
			Deletions: fd.Deletions,
			Hunks:     []diffHunkJSON{},
		}

		if fd.IsBinary {
			files = append(files, file)
			continue
		}

		oldContent, newContent, ok := s.readDiffBlobs(st, repoID, fd)
		if !ok {
			// Blob is legitimately unavailable (older/partial history): report
			// the file's status without hunks rather than failing the request.
			files = append(files, file)
			continue
		}

		perFileBudget := min(maxDiffLinesPerFile, responseBudget)
		hunks, emitted, truncated := buildHunks(oldContent, newContent, diffContextLines, perFileBudget)
		file.Hunks = hunks
		file.Truncated = truncated
		responseBudget = max(0, responseBudget-emitted)

		files = append(files, file)
	}

	return files
}

// readDiffBlobs reads the old and new blob content for a file diff, tolerating
// a missing blob (empty hash for an added/deleted file, or an unreadable one)
// rather than failing the whole request. ok is false only when a non-empty
// blob hash could not be read.
func (s *Server) readDiffBlobs(st *store.Store, repoID string, fd treediff.FileDiff) (oldContent, newContent []byte, ok bool) {
	if fd.OldBlob != "" {
		content, err := st.ReadBlob(fd.OldBlob)
		if err != nil {
			s.logf("read old blob %s for %s in %s: %v", fd.OldBlob, fd.Path, repoID, err)
			return nil, nil, false
		}
		oldContent = content
	}
	if fd.NewBlob != "" {
		content, err := st.ReadBlob(fd.NewBlob)
		if err != nil {
			s.logf("read new blob %s for %s in %s: %v", fd.NewBlob, fd.Path, repoID, err)
			return nil, nil, false
		}
		newContent = content
	}
	return oldContent, newContent, true
}

// buildHunks computes the line-level diff between oldContent and newContent,
// groups it into unified-diff-style hunks with context lines of surrounding
// context, and caps the emitted lines at budget. truncated reports whether the
// full (untruncated) diff needed more than budget lines, computed up front so
// hitting the cap exactly at the final line is never misreported as a cut.
func buildHunks(oldContent, newContent []byte, context, budget int) (hunks []diffHunkJSON, emitted int, truncated bool) {
	hunks = []diffHunkJSON{}
	oldLines := splitStoredLines(oldContent)
	newLines := splitStoredLines(newContent)
	ops := diff.LineDiff(oldContent, newContent)
	groups := groupOpcodes(ops, context)

	total := 0
	for _, group := range groups {
		for _, op := range group {
			total += opLineCount(op)
		}
	}
	truncated = total > budget

	for _, group := range groups {
		if len(group) == 0 {
			continue
		}
		first, last := group[0], group[len(group)-1]
		oldStart, oldLen := hunkRange(first.I1, last.I2)
		newStart, newLen := hunkRange(first.J1, last.J2)
		h := diffHunkJSON{OldStart: oldStart, OldLines: oldLen, NewStart: newStart, NewLines: newLen, Lines: []diffLineJSON{}}

		fits := true
		for _, op := range group {
			for _, ln := range opLines(op, oldLines, newLines) {
				if emitted >= budget {
					fits = false
					break
				}
				h.Lines = append(h.Lines, ln)
				emitted++
			}
			if !fits {
				break
			}
		}
		hunks = append(hunks, h)
		if !fits {
			break
		}
	}
	return hunks, emitted, truncated
}

// hunkRange converts a half-open [start,end) 0-based line range into the
// 1-based (start, length) pair unified diffs use in hunk headers. A
// zero-length range (a pure insertion or deletion point) reports its 0-based
// position as-is, per unified-diff convention (e.g. "@@ -3,0 +4,2 @@" for an
// insertion after old line 3).
func hunkRange(start, end int) (int, int) {
	length := end - start
	if length <= 0 {
		return start, 0
	}
	return start + 1, length
}

// opLineCount returns how many diff lines one opcode contributes.
func opLineCount(op diff.Opcode) int {
	switch op.Tag {
	case "equal", "delete":
		return op.I2 - op.I1
	case "insert":
		return op.J2 - op.J1
	case "replace":
		return (op.I2 - op.I1) + (op.J2 - op.J1)
	default:
		return 0
	}
}

// opLines expands one opcode into its diffLineJSON rows. A replace emits its
// deletions before its insertions, matching conventional unified-diff order.
func opLines(op diff.Opcode, oldLines, newLines []string) []diffLineJSON {
	switch op.Tag {
	case "equal":
		out := make([]diffLineJSON, 0, op.I2-op.I1)
		for k := op.I1; k < op.I2; k++ {
			j := op.J1 + (k - op.I1)
			out = append(out, diffLineJSON{Kind: "context", OldNumber: k + 1, NewNumber: j + 1, Content: oldLines[k]})
		}
		return out
	case "delete":
		out := make([]diffLineJSON, 0, op.I2-op.I1)
		for k := op.I1; k < op.I2; k++ {
			out = append(out, diffLineJSON{Kind: "delete", OldNumber: k + 1, Content: oldLines[k]})
		}
		return out
	case "insert":
		out := make([]diffLineJSON, 0, op.J2-op.J1)
		for k := op.J1; k < op.J2; k++ {
			out = append(out, diffLineJSON{Kind: "add", NewNumber: k + 1, Content: newLines[k]})
		}
		return out
	case "replace":
		out := make([]diffLineJSON, 0, (op.I2-op.I1)+(op.J2-op.J1))
		for k := op.I1; k < op.I2; k++ {
			out = append(out, diffLineJSON{Kind: "delete", OldNumber: k + 1, Content: oldLines[k]})
		}
		for k := op.J1; k < op.J2; k++ {
			out = append(out, diffLineJSON{Kind: "add", NewNumber: k + 1, Content: newLines[k]})
		}
		return out
	default:
		return nil
	}
}

// groupOpcodes mirrors Python difflib's SequenceMatcher.get_grouped_opcodes:
// it collapses an "equal" run longer than 2*context down to a leading/trailing
// slice of exactly context lines, splitting the opcode stream into separate
// hunks at long unchanged stretches. Two changes whose surrounding unchanged
// region is short enough end up sharing one hunk, which is what "merge changes
// whose context regions overlap" means in unified-diff terms.
func groupOpcodes(ops []diff.Opcode, context int) [][]diff.Opcode {
	if len(ops) == 0 {
		return nil
	}
	codes := make([]diff.Opcode, len(ops))
	copy(codes, ops)

	if codes[0].Tag == "equal" {
		op := codes[0]
		codes[0] = diff.Opcode{
			Tag: "equal",
			I1:  max(op.I1, op.I2-context), I2: op.I2,
			J1: max(op.J1, op.J2-context), J2: op.J2,
		}
	}
	if last := len(codes) - 1; codes[last].Tag == "equal" {
		op := codes[last]
		codes[last] = diff.Opcode{
			Tag: "equal",
			I1:  op.I1, I2: min(op.I2, op.I1+context),
			J1: op.J1, J2: min(op.J2, op.J1+context),
		}
	}

	nn := context + context
	var groups [][]diff.Opcode
	var group []diff.Opcode
	for _, op := range codes {
		if op.Tag == "equal" && op.I2-op.I1 > nn {
			group = append(group, diff.Opcode{
				Tag: "equal",
				I1:  op.I1, I2: min(op.I2, op.I1+context),
				J1: op.J1, J2: min(op.J2, op.J1+context),
			})
			groups = append(groups, group)
			group = nil
			op = diff.Opcode{
				Tag: "equal",
				I1:  max(op.I1, op.I2-context), I2: op.I2,
				J1: max(op.J1, op.J2-context), J2: op.J2,
			}
		}
		group = append(group, op)
	}
	if len(group) > 0 && !(len(group) == 1 && group[0].Tag == "equal") {
		groups = append(groups, group)
	}
	return groups
}
