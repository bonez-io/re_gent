package pipeline

import (
	"strings"
	"testing"
)

func TestExtractURLs_ClassifiesAndTrims(t *testing.T) {
	text := `See https://github.com/bonez-io/re_gent/pull/95, (https://github.com/bonez-io/re_gent/issues/12) and
https://linear.app/bonez/issue/1SI-1051/rfc plus https://example.com/docs. Also https://gitlab.com/g/p/-/merge_requests/3!`
	got := extractURLs(text, "s1")
	want := map[string]string{
		"pull_request:bonez-io/re_gent#95": "https://github.com/bonez-io/re_gent/pull/95",
		"issue:bonez-io/re_gent#12":        "https://github.com/bonez-io/re_gent/issues/12",
		"issue:1SI-1051":                   "https://linear.app/bonez/issue/1SI-1051/rfc",
		"link:example.com/docs":            "https://example.com/docs",
		"merge_request:g/p!3":              "https://gitlab.com/g/p/-/merge_requests/3",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entities: %#v", len(got), got)
	}
	for _, e := range got {
		if want[e.Type+":"+e.Name] != e.Ref || e.EvidenceStepID != "s1" {
			t.Errorf("unexpected %#v", e)
		}
	}
}

func TestExtractGit(t *testing.T) {
	got := extractGit("git checkout -b feat/search && git commit -m x", "[feat/search 9f8e7d6] x\n 2 files changed", "s2")
	var names []string
	for _, e := range got {
		names = append(names, e.Type+":"+e.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "commit:9f8e7d6") || !strings.Contains(joined, "branch:feat/search") {
		t.Fatalf("got %v", names)
	}
	if extractGit("ls -la", "[x abc1234] y", "s") != nil {
		t.Fatal("non-git commands must not yield commits")
	}
	if e := extractGit("git push -u origin fix/thing", "", "s"); len(e) != 1 || e[0].Name != "fix/thing" {
		t.Fatalf("push branch: %#v", e)
	}
}

func TestDedupeEntities(t *testing.T) {
	in := []EntityView{
		{Type: "link", Name: "b", Ref: "https://b"},
		{Type: "concept", Name: "Retry", EvidenceStepID: "1"},
		{Type: "concept", Name: "retry", EvidenceStepID: "2"},
		{Type: "link", Name: "b", Ref: "https://b"},
	}
	got := dedupeEntities(in)
	if len(got) != 2 || got[0].Type != "concept" || got[0].EvidenceStepID != "1" || got[1].Type != "link" {
		t.Fatalf("got %#v", got)
	}
}

func TestRenderHunk(t *testing.T) {
	old := []byte("a\nb\nc\nd\n")
	new := []byte("a\nB\nc\nd\ne\n")
	h := renderHunk("f", old, new, 0)
	for _, want := range []string{"-b", "+B", "+e", "@@ -2 +2 @@"} {
		if !strings.Contains(h, want) {
			t.Errorf("hunk missing %q:\n%s", want, h)
		}
	}
	if renderHunk("bin", []byte("a\x00b"), []byte("c"), 0) != "(binary)" {
		t.Error("binary content should not be diffed")
	}
	long := strings.Repeat("x\n", 500)
	if h := renderHunk("f", nil, []byte(long), 100); !strings.Contains(h, "truncated") || len(h) > 200 {
		t.Errorf("limit not applied: %d bytes", len(h))
	}
}

func TestScrubber(t *testing.T) {
	s, err := NewScrubber([]string{`client-\w+`})
	if err != nil {
		t.Fatal(err)
	}
	out := s.ScrubString("token for client-acme is sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-AA")
	if strings.Contains(out, "client-acme") || strings.Contains(out, "sk-ant-api03-abc") {
		t.Fatalf("not scrubbed: %s", out)
	}
	if _, err := NewScrubber([]string{"("}); err == nil {
		t.Fatal("bad pattern must be an error")
	}
}
