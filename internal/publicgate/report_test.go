package publicgate

import (
	"strings"
	"testing"
)

func TestReport_RecordAndString(t *testing.T) {
	r := NewReport()
	r.Record(Decision{Action: Accept})
	r.Record(Decision{Action: Accept})
	r.Record(Decision{Action: Reject, Reasons: []string{`path "secrets.env" is not in the public project's allowlist`}})
	r.Record(Decision{Action: Rewrite, Reasons: []string{"tool_result: redacted aws_access_key_id"}})
	r.Record(Decision{Action: Rewrite, Reasons: []string{"tool_result: redacted aws_access_key_id"}})

	if r.Accepted != 2 || r.Rejected != 1 || r.Rewritten != 2 {
		t.Fatalf("counts = accepted:%d rejected:%d rewritten:%d, want 2/1/2", r.Accepted, r.Rejected, r.Rewritten)
	}

	reasons := r.Reasons()
	if len(reasons) != 2 {
		t.Fatalf("expected 2 distinct reasons, got %d: %+v", len(reasons), reasons)
	}
	byReason := make(map[string]int, len(reasons))
	for _, rc := range reasons {
		byReason[rc.Reason] = rc.Count
	}
	if byReason[`path "secrets.env" is not in the public project's allowlist`] != 1 {
		t.Fatalf("expected the reject reason counted once, got %+v", reasons)
	}
	if byReason["tool_result: redacted aws_access_key_id"] != 2 {
		t.Fatalf("expected the rewrite reason counted twice, got %+v", reasons)
	}

	s := r.String()
	if !strings.Contains(s, "accepted 2, rejected 1, rewritten 2") {
		t.Fatalf("String() missing summary line: %q", s)
	}
	if !strings.Contains(s, "aws_access_key_id (x2)") {
		t.Fatalf("String() missing counted reason: %q", s)
	}
}

func TestReport_Empty(t *testing.T) {
	r := NewReport()
	if got, want := r.String(), "accepted 0, rejected 0, rewritten 0"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if len(r.Reasons()) != 0 {
		t.Fatalf("expected no reasons, got %+v", r.Reasons())
	}
}
