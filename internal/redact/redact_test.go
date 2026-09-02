package redact

import (
	"strings"
	"testing"
)

// detectCase is a single positive or negative detector vector. When
// wantKind is empty, content must produce zero findings.
type detectCase struct {
	name      string
	content   string
	wantKind  string
	wantMatch string // exact substring expected at the finding's Start:End
}

// Example secrets are assembled at runtime so no full-format token ever sits
// in source: GitHub push protection scans committed blobs and would block
// the push otherwise. The detectors see the joined strings exactly as they
// would see a real leak.
var (
	slackExample  = "xoxb-" + "111111111111-222222222222-abcdefghijklmnopqrstuvwx"
	stripeExample = "sk_live_" + "abcdefghijklmnopqrstuvwx"
)

func TestDetect(t *testing.T) {
	cases := []detectCase{
		{
			name:      "aws access key id",
			content:   "export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE",
			wantKind:  "aws_access_key_id",
			wantMatch: "AKIAIOSFODNN7EXAMPLE",
		},
		{
			name:      "aws secret access key",
			content:   "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			wantKind:  "aws_secret_access_key",
			wantMatch: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		},
		{
			name:      "github classic token",
			content:   "curl -H 'Authorization: token ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ'",
			wantKind:  "github_token",
			wantMatch: "ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ",
		},
		{
			name:      "github fine-grained pat",
			content:   "GITHUB_TOKEN=github_pat_abcdefghijklmnopqrstuv_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456",
			wantKind:  "github_pat",
			wantMatch: "github_pat_abcdefghijklmnopqrstuv_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456",
		},
		{
			name:      "gitlab pat",
			content:   "GITLAB_TOKEN=glpat-abcdefghijklmnopqrst",
			wantKind:  "gitlab_pat",
			wantMatch: "glpat-abcdefghijklmnopqrst",
		},
		{
			name:      "slack token",
			content:   "SLACK_BOT_TOKEN=" + slackExample,
			wantKind:  "slack_token",
			wantMatch: slackExample,
		},
		{
			name:      "stripe live key",
			content:   "STRIPE_KEY=" + stripeExample,
			wantKind:  "stripe_key",
			wantMatch: stripeExample,
		},
		{
			name:      "google api key",
			content:   "GOOGLE_API_KEY=AIzaabcdefghijklmnopqrstuvwxyzABCDEFGHI",
			wantKind:  "google_api_key",
			wantMatch: "AIzaabcdefghijklmnopqrstuvwxyzABCDEFGHI",
		},
		{
			name:      "anthropic key",
			content:   "ANTHROPIC_API_KEY=sk-ant-api03-abcdefghijklmnopqrstuvwx",
			wantKind:  "anthropic_api_key",
			wantMatch: "sk-ant-api03-abcdefghijklmnopqrstuvwx",
		},
		{
			name:      "openai key",
			content:   "OPENAI_API_KEY=sk-abcdefghijklmnopqrstuvwx",
			wantKind:  "openai_api_key",
			wantMatch: "sk-abcdefghijklmnopqrstuvwx",
		},
		{
			name:      "regent pat",
			content:   "REGENT_TOKEN=rgt_pat_abcdefghijklmnopqrstuvwx",
			wantKind:  "regent_token",
			wantMatch: "rgt_pat_abcdefghijklmnopqrstuvwx",
		},
		{
			name: "private key pem",
			content: "before\n" +
				"-----BEGIN RSA PRIVATE KEY-----\n" +
				"MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu\n" +
				"-----END RSA PRIVATE KEY-----\n" +
				"after",
			wantKind: "private_key_pem",
			wantMatch: "-----BEGIN RSA PRIVATE KEY-----\n" +
				"MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu\n" +
				"-----END RSA PRIVATE KEY-----",
		},
		{
			name: "jwt",
			content: "Authorization: Bearer " +
				"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IlRlc3QifQ.abcdefghijklmnopqrstuvwxyzABCDEFGHIJ0123",
			wantKind:  "jwt",
			wantMatch: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IlRlc3QifQ.abcdefghijklmnopqrstuvwxyzABCDEFGHIJ0123",
		},
		{
			// Not at the start of a line (unlike the dotenv case below), so
			// this exercises the generic assignment detector rather than
			// the whole-line dotenv one.
			name:      "generic secret assignment",
			content:   `note: password = "Tr0ub4dor&3xyz" was rotated`,
			wantKind:  "generic_secret_assignment",
			wantMatch: "Tr0ub4dor&3xyz",
		},
		{
			name:      "dotenv secret",
			content:   "DB_PASSWORD=Sup3rSecretValue123",
			wantKind:  "dotenv_secret",
			wantMatch: "Sup3rSecretValue123",
		},

		// --- negative vectors: must NOT match anything ---
		{
			name:     "git commit sha (40 hex)",
			content:  "fixes regression introduced in e3b0c44298fc1c149afbf4c8996fb92427ae41e4",
			wantKind: "",
		},
		{
			name:     "blake3 hash (64 hex)",
			content:  "object 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			wantKind: "",
		},
		{
			name:     "base64 image data uri",
			content:  "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
			wantKind: "",
		},
		{
			name:     "normal prose",
			content:  "The quick brown fox jumps over the lazy dog while the team reviewed the pull request together.",
			wantKind: "",
		},
		{
			name:     "plain url",
			content:  "See https://example.com/docs/getting-started?ref=readme&utm_source=github for details.",
			wantKind: "",
		},
		{
			name:     "placeholder password value is not flagged",
			content:  "password=changeme",
			wantKind: "",
		},
		{
			name:     "repeated-char value is not flagged",
			content:  "password=aaaaaaaa",
			wantKind: "",
		},
		{
			name:     "short generic value under threshold",
			content:  "token=short",
			wantKind: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := Detect([]byte(tc.content))
			if tc.wantKind == "" {
				if len(findings) != 0 {
					t.Fatalf("expected no findings, got %+v", findings)
				}
				return
			}
			if len(findings) == 0 {
				t.Fatalf("expected a %q finding, got none", tc.wantKind)
			}
			var found *Finding
			for i := range findings {
				if findings[i].Kind == tc.wantKind {
					found = &findings[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("expected a finding of kind %q, got %+v", tc.wantKind, findings)
			}
			got := tc.content[found.Start:found.End]
			if got != tc.wantMatch {
				t.Fatalf("matched text = %q, want %q", got, tc.wantMatch)
			}
			if found.Preview == "" {
				t.Fatalf("finding has empty preview")
			}
			if found.Preview == tc.wantMatch {
				t.Fatalf("preview %q must not equal the full secret", found.Preview)
			}
			if len(tc.wantMatch) > 8 && strings.Contains(found.Preview, tc.wantMatch[4:len(tc.wantMatch)-3]) {
				t.Fatalf("preview %q leaks the middle of the secret %q", found.Preview, tc.wantMatch)
			}
		})
	}
}

// TestDetect_CombinedNegatives makes sure the negative vectors don't
// interact badly (e.g. overlap resolution accidentally promoting one) when
// scanned together in one blob, which is closer to how a real tool result
// looks.
func TestDetect_CombinedNegatives(t *testing.T) {
	content := strings.Join([]string{
		"commit e3b0c44298fc1c149afbf4c8996fb92427ae41e4 fixed the bug",
		"blob hash 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef stored",
		"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
		"The quick brown fox jumps over the lazy dog.",
		"https://example.com/docs/getting-started?ref=readme&utm_source=github",
	}, "\n")

	findings := Detect([]byte(content))
	if len(findings) != 0 {
		t.Fatalf("expected no findings in combined negative content, got %+v", findings)
	}
}

func TestDetect_PriorityResolvesOverlap(t *testing.T) {
	// A stripe key embedded in a generic "token=" assignment should be
	// reported once, as stripe_key, not as generic_secret_assignment too.
	content := "token=" + stripeExample
	findings := Detect([]byte(content))
	if len(findings) != 1 {
		t.Fatalf("expected exactly 1 finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Kind != "stripe_key" {
		t.Fatalf("expected stripe_key to win priority, got %q", findings[0].Kind)
	}
}

func TestRedact(t *testing.T) {
	content := []byte("AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE and password = \"Tr0ub4dor&3xyz\" done")
	out, findings := Redact(content, Options{})

	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d: %+v", len(findings), findings)
	}
	outStr := string(out)
	if strings.Contains(outStr, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("redacted output still contains the AWS key: %q", outStr)
	}
	if strings.Contains(outStr, "Tr0ub4dor&3xyz") {
		t.Fatalf("redacted output still contains the password: %q", outStr)
	}
	if !strings.Contains(outStr, "[REDACTED:aws_access_key_id]") {
		t.Fatalf("expected aws_access_key_id marker in output: %q", outStr)
	}
	if !strings.Contains(outStr, "[REDACTED:generic_secret_assignment]") {
		t.Fatalf("expected generic_secret_assignment marker in output: %q", outStr)
	}
	if !strings.Contains(outStr, "done") || !strings.Contains(outStr, "and") {
		t.Fatalf("redaction should not disturb surrounding text: %q", outStr)
	}
}

func TestRedact_KindFilter(t *testing.T) {
	content := []byte("AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE and password = \"Tr0ub4dor&3xyz\" done")
	out, findings := Redact(content, Options{Kinds: []string{"aws_access_key_id"}})

	if len(findings) != 1 || findings[0].Kind != "aws_access_key_id" {
		t.Fatalf("expected only the aws_access_key_id finding, got %+v", findings)
	}
	outStr := string(out)
	if strings.Contains(outStr, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("redacted output still contains the AWS key: %q", outStr)
	}
	if !strings.Contains(outStr, "Tr0ub4dor&3xyz") {
		t.Fatalf("password should have been left alone by the kind filter: %q", outStr)
	}
}

func TestRedact_NoFindings(t *testing.T) {
	content := []byte("nothing to see here")
	out, findings := Redact(content, Options{})
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %+v", findings)
	}
	if string(out) != string(content) {
		t.Fatalf("expected content unchanged, got %q", out)
	}
}

func TestMaskPreview(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ab", "**"},
		{"abcdefg", "a…g"},
		{"AKIAIOSFODNN7EXAMPLE", "AKIA…PLE"},
	}
	for _, tc := range cases {
		got := maskPreview([]byte(tc.in))
		if got != tc.want {
			t.Errorf("maskPreview(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShannonEntropy(t *testing.T) {
	if got := shannonEntropy("aaaaaaaa"); got != 0 {
		t.Errorf("shannonEntropy(all-same) = %v, want 0", got)
	}
	if got := shannonEntropy("Tr0ub4dor&3xyz"); got < 2.0 {
		t.Errorf("shannonEntropy(mixed) = %v, want >= 2.0", got)
	}
}
