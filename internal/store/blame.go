package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/regent-vcs/regent/internal/diff"
)

// BlameMap tracks per-line provenance for a file
type BlameMap struct {
	Lines []Hash `json:"lines"` // one step hash per line
}

// ReadBlame reads a blame map from the object store
func (s *Store) ReadBlame(h Hash) (*BlameMap, error) {
	data, err := s.ReadBlob(h)
	if err != nil {
		return nil, err
	}

	var bm BlameMap
	if err := json.Unmarshal(data, &bm); err != nil {
		return nil, fmt.Errorf("decode blame map: %w", err)
	}

	return &bm, nil
}

// WriteBlame writes a blame map to the object store
func (s *Store) WriteBlame(bm *BlameMap) (Hash, error) {
	data, err := json.Marshal(bm)
	if err != nil {
		return "", fmt.Errorf("encode blame map: %w", err)
	}
	return s.WriteBlob(data)
}

// ComputeBlame builds a new blame map given old/new content and old blame
// Lines unchanged inherit old attribution; modified lines get currentStep
func ComputeBlame(oldContent, newContent []byte, oldBlame *BlameMap, currentStep Hash) *BlameMap {
	ops := diff.LineDiff(oldContent, newContent)

	newBlame := &BlameMap{Lines: make([]Hash, 0)}

	for _, op := range ops {
		switch op.Tag {
		case "equal":
			// Preserved lines keep their old attribution
			for i := op.I1; i < op.I2; i++ {
				if oldBlame != nil && i < len(oldBlame.Lines) {
					newBlame.Lines = append(newBlame.Lines, oldBlame.Lines[i])
				} else {
					// File existed but had no blame (pre-Phase 3)
					newBlame.Lines = append(newBlame.Lines, currentStep)
				}
			}

		case "insert", "replace":
			// New or modified lines belong to currentStep
			for j := op.J1; j < op.J2; j++ {
				newBlame.Lines = append(newBlame.Lines, currentStep)
			}

		case "delete":
			// Deleted lines contribute nothing to new blame
		}
	}

	return newBlame
}

// BlameKey generates deterministic path for blame storage
func BlameKey(stepHash Hash, filePath string) string {
	// Hash the file path for consistent directory structure
	h := sha256.Sum256([]byte(filePath))
	pathHash := hex.EncodeToString(h[:])[:16] // First 16 chars
	return filepath.Join("blame", string(stepHash), pathHash)
}

// WriteBlameForFile writes blame map for a specific file at a step.
//
// The write is atomic (temp file + rename) rather than a truncate-in-place.
// `rgt repair blame` rewrites every sidecar in a store, and a run killed
// halfway through a truncating write would leave a partial JSON file behind:
// `rgt blame` on that file then fails to decode, and re-running the repair
// cannot help because the damage is in the output, not the input. With a rename
// every reader sees either the whole old map or the whole new one.
func (s *Store) WriteBlameForFile(stepHash Hash, filePath string, blameMap *BlameMap) error {
	data, err := json.Marshal(blameMap)
	if err != nil {
		return err
	}

	key := BlameKey(stepHash, filePath)
	blamePath := filepath.Join(s.Root, key)

	if err := os.MkdirAll(filepath.Dir(blamePath), 0755); err != nil {
		return err
	}

	return atomicWriteFile(blamePath, data, 0o644)
}

// BlameForFileMatches reports whether the stored sidecar for (stepHash,
// filePath) is byte-identical to blameMap. A missing sidecar does not match.
//
// Repair uses this to tell "rewrote it" from "left it alone" without writing
// first, and to stay idempotent: a second run recomputes the same maps, finds
// them identical, and touches no files at all.
func (s *Store) BlameForFileMatches(stepHash Hash, filePath string, blameMap *BlameMap) (bool, error) {
	data, err := json.Marshal(blameMap)
	if err != nil {
		return false, err
	}

	existing, err := os.ReadFile(filepath.Join(s.Root, BlameKey(stepHash, filePath)))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	return bytes.Equal(existing, data), nil
}

// ReadBlameForFile reads blame map for a specific file at a step
func (s *Store) ReadBlameForFile(stepHash Hash, filePath string) (*BlameMap, error) {
	key := BlameKey(stepHash, filePath)
	blamePath := filepath.Join(s.Root, key)

	data, err := os.ReadFile(blamePath)
	if err != nil {
		return nil, err
	}

	var blameMap BlameMap
	if err := json.Unmarshal(data, &blameMap); err != nil {
		return nil, err
	}

	return &blameMap, nil
}
