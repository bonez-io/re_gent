package diff

import (
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// Opcode represents a diff operation
type Opcode struct {
	Tag    string // "equal", "insert", "replace", "delete"
	I1, I2 int    // old range [I1, I2)
	J1, J2 int    // new range [J1, J2)
}

// LineDiff computes line-level diff between old and new content
func LineDiff(oldContent, newContent []byte) []Opcode {
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)

	// Encode each distinct line as one rune ourselves, then diff the runes.
	//
	// The library's own line mode (DiffLinesToChars / DiffLinesToRunes paired
	// with DiffCharsToLines) does not round-trip reliably here: mapping the
	// chunks back to text returned neighbouring lines, so counting newlines in
	// them was counting the wrong thing. Two files differing only at line 3
	// diffed as "lines 1-3 are equal" — including the changed line — then an
	// insert of line 5, then the rest from line 4. Same lines, wrong order, one
	// line too many.
	//
	// That is what produced blame maps one entry longer than the file, and
	// attribution one line late: `rgt blame` named the line *after* the one an
	// edit actually touched. Found on a real package.json, where bumping
	// "version" blamed the "description" beneath it.
	//
	// With our own encoding the counts are exact by construction — one rune in,
	// one line out — and nothing has to be mapped back, because callers index
	// their own line slices with these offsets and never need the contents.
	a, b := encodeLines(oldLines, newLines)

	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMainRunes(a, b, false)

	// Convert diffs to opcode format
	return diffsToOpcodes(diffs, oldLines, newLines)
}

// encodeLines maps every distinct line to a distinct rune, so a rune diff is a
// line diff. Surrogates are skipped because they cannot appear alone in a Go
// string, which would corrupt the encoding for files with many unique lines.
func encodeLines(oldLines, newLines []string) ([]rune, []rune) {
	assigned := make(map[string]rune)
	next := rune(1)

	encode := func(lines []string) []rune {
		out := make([]rune, len(lines))
		for i, line := range lines {
			r, seen := assigned[line]
			if !seen {
				if next >= 0xD800 && next <= 0xDFFF {
					next = 0xE000
				}
				r = next
				assigned[line] = r
				next++
			}
			out[i] = r
		}
		return out
	}

	return encode(oldLines), encode(newLines)
}

func splitLines(content []byte) []string {
	if len(content) == 0 {
		return []string{}
	}

	// Split on \n, preserve empty lines
	lines := strings.Split(string(content), "\n")

	// Remove trailing empty line if file ends with \n
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	return lines
}

func diffsToOpcodes(diffs []diffmatchpatch.Diff, oldLines, newLines []string) []Opcode {
	var opcodes []Opcode
	i1, i2 := 0, 0 // indices in old
	j1, j2 := 0, 0 // indices in new

	for _, diff := range diffs {
		// One rune per line, by our own encoding: exact.
		lineCount := len([]rune(diff.Text))

		switch diff.Type {
		case diffmatchpatch.DiffEqual:
			i1, i2 = i2, i2+lineCount
			j1, j2 = j2, j2+lineCount
			opcodes = append(opcodes, Opcode{
				Tag: "equal",
				I1:  i1,
				I2:  i2,
				J1:  j1,
				J2:  j2,
			})

		case diffmatchpatch.DiffDelete:
			i1, i2 = i2, i2+lineCount
			opcodes = append(opcodes, Opcode{
				Tag: "delete",
				I1:  i1,
				I2:  i2,
				J1:  j1,
				J2:  j1, // no new lines
			})

		case diffmatchpatch.DiffInsert:
			j1, j2 = j2, j2+lineCount
			opcodes = append(opcodes, Opcode{
				Tag: "insert",
				I1:  i1,
				I2:  i1, // no old lines
				J1:  j1,
				J2:  j2,
			})
		}
	}

	// Merge consecutive delete+insert into replace
	return mergeReplaces(opcodes)
}

func mergeReplaces(opcodes []Opcode) []Opcode {
	if len(opcodes) == 0 {
		return opcodes
	}

	var result []Opcode
	i := 0

	for i < len(opcodes) {
		op := opcodes[i]

		// Check if current is delete and next is insert (= replace)
		if i+1 < len(opcodes) &&
			op.Tag == "delete" &&
			opcodes[i+1].Tag == "insert" {

			// Merge into replace
			next := opcodes[i+1]
			result = append(result, Opcode{
				Tag: "replace",
				I1:  op.I1,
				I2:  op.I2,
				J1:  next.J1,
				J2:  next.J2,
			})
			i += 2
		} else {
			result = append(result, op)
			i++
		}
	}

	return result
}
