package style

import (
	"fmt"
	"io"
	"strings"
)

// Flow is the compact terminal surface used by onboarding commands. It is
// intentionally line-oriented rather than full-screen: it stays readable in
// CI, curl-pipe-shell installs, and copied support logs while still presenting
// one coherent re_gent UI in a terminal.
type Flow struct {
	out io.Writer
}

func NewFlow(out io.Writer) *Flow {
	if out == nil {
		out = io.Discard
	}
	return &Flow{out: out}
}

// Header opens an onboarding flow with the existing re_gent brand palette.
func (f *Flow) Header(action, subject string) {
	fmt.Fprintf(f.out, "\n  %s %s %s\n", Brand("re_gent"), DimText("/"), Title(action))
	if subject != "" {
		fmt.Fprintf(f.out, "  %s\n", DimText(subject))
	}
	fmt.Fprintln(f.out)
}

func (f *Flow) Step(label string) {
	fmt.Fprintf(f.out, "  %s\n", Success(label))
}

func (f *Flow) Warning(label string) {
	fmt.Fprintf(f.out, "  %s\n", Warning(label))
}

func (f *Flow) Detail(label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	// Pad before adding ANSI styling; otherwise escape bytes consume the field
	// width and colored output loses the alignment used by plain output.
	fmt.Fprintf(f.out, "  %s %s\n", Label(fmt.Sprintf("%-9s", label)), value)
}

func (f *Flow) Complete(label string) {
	fmt.Fprintf(f.out, "\n  %s\n", Success(Title(label)))
}

func (f *Flow) Next(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	fmt.Fprintf(f.out, "  %s %s\n", Label("Next"), text)
}

func (f *Flow) Hint(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	fmt.Fprintf(f.out, "  %s\n", DimText(text))
}

func (f *Flow) End() { fmt.Fprintln(f.out) }
