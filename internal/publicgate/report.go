package publicgate

import (
	"fmt"
	"strings"
)

// Report summarizes the Decisions a Checker produced over a session, for
// the CLI to print (e.g. after `git push` finishes, or at the end of a
// capture run against a public project).
type Report struct {
	Accepted  int
	Rejected  int
	Rewritten int

	reasonCounts map[string]int
	reasonOrder  []string
}

// NewReport returns an empty Report ready for Record calls.
func NewReport() *Report {
	return &Report{reasonCounts: make(map[string]int)}
}

// Record folds one Decision into the report: it counts the Action, and
// counts each of the Decision's Reasons (so e.g. "redacted
// aws_access_key_id" occurring 5 times across a session shows up once,
// with a x5 count, rather than 5 lines).
func (r *Report) Record(d Decision) {
	switch d.Action {
	case Accept:
		r.Accepted++
	case Reject:
		r.Rejected++
	case Rewrite:
		r.Rewritten++
	}

	if r.reasonCounts == nil {
		r.reasonCounts = make(map[string]int)
	}
	for _, reason := range d.Reasons {
		if r.reasonCounts[reason] == 0 {
			r.reasonOrder = append(r.reasonOrder, reason)
		}
		r.reasonCounts[reason]++
	}
}

// ReasonCount is one distinct reason string and how many recorded
// Decisions carried it.
type ReasonCount struct {
	Reason string
	Count  int
}

// Reasons returns the distinct reasons recorded so far with their counts,
// in first-seen order. Callers that want a different presentation than
// String() (e.g. JSON) can use this directly.
func (r *Report) Reasons() []ReasonCount {
	out := make([]ReasonCount, 0, len(r.reasonOrder))
	for _, reason := range r.reasonOrder {
		out = append(out, ReasonCount{Reason: reason, Count: r.reasonCounts[reason]})
	}
	return out
}

// String renders a short, stable, human-readable summary suitable for
// direct CLI output, e.g.:
//
//	accepted 12, rejected 2, rewritten 3
//	  path "secrets.env" is not in the public project's allowlist (x1)
//	  tool_result: redacted aws_access_key_id (x2)
func (r *Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "accepted %d, rejected %d, rewritten %d", r.Accepted, r.Rejected, r.Rewritten)
	for _, reason := range r.reasonOrder {
		fmt.Fprintf(&b, "\n  %s (x%d)", reason, r.reasonCounts[reason])
	}
	return b.String()
}
