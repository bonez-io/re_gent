package publicgate

import (
	"strings"
	"testing"
)

func TestCheckBlob_FileOutsideAllowlist_Rejects(t *testing.T) {
	root := initTestRepo(t, map[string]string{"tracked.go": "package main"})
	al, err := NewPathAllowlist(root)
	if err != nil {
		t.Fatalf("NewPathAllowlist: %v", err)
	}

	g := Gate(Policy{PathAllowlist: al})
	d := g.CheckBlob(KindFile, "untracked.go", []byte("package main"))

	if d.Action != Reject {
		t.Fatalf("Action = %v, want Reject", d.Action)
	}
	if d.Content != nil {
		t.Fatalf("expected nil Content on reject, got %q", d.Content)
	}
	if len(d.Reasons) == 0 || !strings.Contains(d.Reasons[0], "untracked.go") {
		t.Fatalf("expected reason to name the path, got %v", d.Reasons)
	}
}

func TestCheckBlob_FileWithSecret_Rejects_NeverRewritesFiles(t *testing.T) {
	// Uses a tracked, non-denylisted file (not .env/.pem/.key/id_rsa*)
	// that happens to contain a secret, to isolate the "secret in file
	// content" rule from the path-denylist rule.
	content := []byte("const key = \"AKIAIOSFODNN7EXAMPLE\"")
	root := initTestRepo(t, map[string]string{"settings.go": string(content)})
	al, err := NewPathAllowlist(root)
	if err != nil {
		t.Fatalf("NewPathAllowlist: %v", err)
	}

	g := Gate(Policy{Secrets: true, PathAllowlist: al})
	d := g.CheckBlob(KindFile, "settings.go", content)

	if d.Action != Reject {
		t.Fatalf("Action = %v, want Reject", d.Action)
	}
	if d.Content != nil {
		t.Fatalf("expected nil Content on reject, got %q", d.Content)
	}
	if len(d.Reasons) == 0 || !strings.Contains(d.Reasons[0], "aws_access_key_id") {
		t.Fatalf("expected reason to name the secret kind, got %v", d.Reasons)
	}
	if strings.Contains(d.Reasons[0], "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("reason must not contain the secret itself: %v", d.Reasons)
	}
}

func TestCheckBlob_ToolResultWithSecret_Rewrites(t *testing.T) {
	g := Gate(Policy{Secrets: true})
	content := []byte("here is the key: AKIAIOSFODNN7EXAMPLE, use it")
	d := g.CheckBlob(KindToolResult, "", content)

	if d.Action != Rewrite {
		t.Fatalf("Action = %v, want Rewrite", d.Action)
	}
	if strings.Contains(string(d.Content), "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("rewritten content still contains the secret: %q", d.Content)
	}
	if !strings.Contains(string(d.Content), "[REDACTED:aws_access_key_id]") {
		t.Fatalf("expected redaction marker, got %q", d.Content)
	}
	if len(d.Reasons) == 0 || !strings.Contains(d.Reasons[0], "aws_access_key_id") {
		t.Fatalf("expected reason to name the secret kind, got %v", d.Reasons)
	}
	for _, r := range d.Reasons {
		if strings.Contains(r, "AKIAIOSFODNN7EXAMPLE") {
			t.Fatalf("reason leaks the secret: %q", r)
		}
	}
}

func TestCheckBlob_MessageWithHomePath_Rewrites(t *testing.T) {
	g := Gate(Policy{HomePaths: true, Homes: []string{"/Users/shay"}, Usernames: []string{"shay"}})
	content := []byte("wrote to /Users/shay/Projects/re_gent/main.go for shay")
	d := g.CheckBlob(KindMessage, "", content)

	if d.Action != Rewrite {
		t.Fatalf("Action = %v, want Rewrite", d.Action)
	}
	if strings.Contains(string(d.Content), "/Users/shay") {
		t.Fatalf("rewritten content still contains the home path: %q", d.Content)
	}
	if !strings.Contains(string(d.Content), "~/Projects/re_gent/main.go") {
		t.Fatalf("expected scrubbed path preserved, got %q", d.Content)
	}
}

func TestCheckBlob_CleanContent_Accepts(t *testing.T) {
	g := Gate(Policy{Secrets: true, HomePaths: true})
	content := []byte("nothing sensitive here")
	d := g.CheckBlob(KindMessage, "", content)

	if d.Action != Accept {
		t.Fatalf("Action = %v, want Accept", d.Action)
	}
	if string(d.Content) != string(content) {
		t.Fatalf("Content = %q, want unchanged %q", d.Content, content)
	}
	if len(d.Reasons) != 0 {
		t.Fatalf("expected no reasons on accept, got %v", d.Reasons)
	}
}

func TestCheckBlob_NilPathAllowlist_FailsClosedForFiles(t *testing.T) {
	g := Gate(Policy{}) // no PathAllowlist configured
	d := g.CheckBlob(KindFile, "anything.go", []byte("package main"))

	if d.Action != Reject {
		t.Fatalf("Action = %v, want Reject (fail closed with no allowlist)", d.Action)
	}
}

func TestCheckBlob_DisabledPolicyIsANoop(t *testing.T) {
	g := Gate(Policy{}) // Secrets and HomePaths both off
	content := []byte("AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE, at /Users/shay/proj")
	d := g.CheckBlob(KindToolResult, "", content)

	if d.Action != Accept {
		t.Fatalf("Action = %v, want Accept when the policy has nothing enabled", d.Action)
	}
	if string(d.Content) != string(content) {
		t.Fatalf("Content = %q, want unchanged", d.Content)
	}
}

func TestAction_String(t *testing.T) {
	cases := map[Action]string{
		Accept:     "accept",
		Reject:     "reject",
		Rewrite:    "rewrite",
		Action(99): "unknown",
	}
	for action, want := range cases {
		if got := action.String(); got != want {
			t.Errorf("Action(%d).String() = %q, want %q", action, got, want)
		}
	}
}
