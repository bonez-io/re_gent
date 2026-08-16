package cli

import (
	"bufio"
	"strings"
	"testing"
)

// There are two confirmation readers in this package and they disagree about
// what "the user pressed enter" looks like.
//
// readAnswer accepts either a newline or a carriage return, and a comment above
// it records why: after a full-screen picker hands the terminal back, the
// keypress arrives as a carriage return, and a newline-only read waits forever
// for a key the user already pressed.
//
// confirmedDefaultYes never got that fix. It reads to a newline, so a carriage
// return either hangs against a live terminal or — as here, against a reader
// that ends — returns an error and discards the answer. The skills prompt is
// behind it, and under `curl | sh` that prompt reads the installing script
// itself, which is exactly the situation the fix was written for.
//
// One reader, accepting both, is the whole of this test's demand.
func TestConfirmPromptAcceptsEitherLineEnding(t *testing.T) {
	cases := []struct {
		input string
		want  bool
		note  string
	}{
		{"y\n", true, "newline, the case that already worked"},
		{"y\r", true, "carriage return — a picker handed the terminal back"},
		{"y\r\n", true, "both, as a Windows terminal sends"},
		{"\n", true, "bare enter takes the default, which is yes"},
		{"\r", true, "bare enter as a carriage return"},
		{"n\n", false, "declining still declines"},
		{"n\r", false, "declining over a carriage return still declines"},
	}

	for _, tc := range cases {
		got, err := confirmedDefaultYes(bufio.NewReader(strings.NewReader(tc.input)))
		if err != nil {
			t.Errorf("confirmedDefaultYes(%q): unexpected error %v (%s)", tc.input, err, tc.note)
			continue
		}
		if got != tc.want {
			t.Errorf("confirmedDefaultYes(%q) = %v, want %v — %s", tc.input, got, tc.want, tc.note)
		}
	}
}
