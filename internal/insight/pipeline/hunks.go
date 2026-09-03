package pipeline

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/bonez-io/re_gent/internal/diff"
	"github.com/bonez-io/re_gent/internal/store"
)

// stepChanges lists the files a step changed against its parent and renders
// a hunk for each text file. limit bounds the rendered bytes per file; the
// caller applies the request-wide budget.
func stepChanges(s *store.Store, parentStepID, stepID string, limit int) (files []string, hunks []HunkView, err error) {
	step, err := s.ReadStep(store.Hash(stepID))
	if err != nil {
		return nil, nil, fmt.Errorf("read step %s: %w", stepID, err)
	}
	tree, err := s.ReadTree(step.Tree)
	if err != nil {
		return nil, nil, fmt.Errorf("read tree: %w", err)
	}
	parent := map[string]store.Hash{}
	if parentStepID != "" {
		if pstep, err := s.ReadStep(store.Hash(parentStepID)); err == nil {
			if ptree, err := s.ReadTree(pstep.Tree); err == nil {
				for _, e := range ptree.Entries {
					parent[e.Path] = e.Blob
				}
			}
		}
	}

	seen := map[string]bool{}
	for _, e := range tree.Entries {
		seen[e.Path] = true
		old, had := parent[e.Path]
		if had && old == e.Blob {
			continue
		}
		files = append(files, e.Path)
		var oldContent []byte
		if had {
			oldContent, _ = s.ReadBlob(old)
		}
		newContent, rerr := s.ReadBlob(e.Blob)
		if rerr != nil {
			continue
		}
		if h := renderHunk(e.Path, oldContent, newContent, limit); h != "" {
			hunks = append(hunks, HunkView{Path: e.Path, Diff: h})
		}
	}
	for path := range parent {
		if !seen[path] {
			files = append(files, path)
			hunks = append(hunks, HunkView{Path: path, Diff: "(deleted)"})
		}
	}
	return files, hunks, nil
}

// renderHunk renders a compact unified-style diff: changed lines with a
// couple of lines of context, cut at limit bytes.
func renderHunk(path string, oldContent, newContent []byte, limit int) string {
	if isBinary(oldContent) || isBinary(newContent) {
		return "(binary)"
	}
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)
	ops := diff.LineDiff(oldContent, newContent)

	const context = 2
	var b strings.Builder
	for _, op := range ops {
		if op.Tag == "equal" {
			continue
		}
		// Context before.
		start := max(op.I1-context, 0)
		fmt.Fprintf(&b, "@@ -%d +%d @@\n", op.I1+1, op.J1+1)
		for i := start; i < op.I1 && i < len(oldLines); i++ {
			b.WriteString(" " + oldLines[i] + "\n")
		}
		if op.Tag == "delete" || op.Tag == "replace" {
			for i := op.I1; i < op.I2 && i < len(oldLines); i++ {
				b.WriteString("-" + oldLines[i] + "\n")
			}
		}
		if op.Tag == "insert" || op.Tag == "replace" {
			for j := op.J1; j < op.J2 && j < len(newLines); j++ {
				b.WriteString("+" + newLines[j] + "\n")
			}
		}
		for i := op.I2; i < op.I2+context && i < len(oldLines); i++ {
			b.WriteString(" " + oldLines[i] + "\n")
		}
		if limit > 0 && b.Len() > limit {
			s := b.String()[:limit]
			return s + "\n… (" + path + " diff truncated)\n"
		}
	}
	return b.String()
}

func splitLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	s := strings.TrimSuffix(string(content), "\n")
	return strings.Split(s, "\n")
}

func isBinary(content []byte) bool {
	if len(content) == 0 {
		return false
	}
	probe := content
	if len(probe) > 8000 {
		probe = probe[:8000]
	}
	return bytes.IndexByte(probe, 0) >= 0
}
