package publicgate

import (
	"fmt"
	"strings"

	"github.com/bonez-io/re_gent/internal/redact"
)

// Blob kinds accepted by Checker.CheckBlob.
const (
	KindToolInput  = "tool_input"
	KindToolResult = "tool_result"
	KindMessage    = "message"
	KindFile       = "file"
)

// Action is the outcome of checking one blob against a Policy.
type Action int

const (
	// Accept: the blob may be stored/uploaded unchanged.
	Accept Action = iota
	// Reject: the blob must not be stored/uploaded at all: either its
	// path isn't in the public project's allowlist, or (for KindFile
	// only) its content contains a detected secret. Files are never
	// rewritten — see CheckBlob's doc comment for why.
	Reject
	// Rewrite: the blob is stored, but as Decision.Content rather than
	// the original bytes — secrets redacted and/or home paths/usernames
	// scrubbed.
	Rewrite
)

// String renders the Action the way Report and log lines do.
func (a Action) String() string {
	switch a {
	case Accept:
		return "accept"
	case Reject:
		return "reject"
	case Rewrite:
		return "rewrite"
	default:
		return "unknown"
	}
}

// Decision is the result of checking one blob against a Policy.
type Decision struct {
	Action Action
	// Content is what should actually be stored: the original bytes for
	// Accept, the redacted/scrubbed bytes for Rewrite, and nil for
	// Reject (nothing is stored).
	Content []byte
	// Reasons explains the decision in human-readable terms — e.g. which
	// path was out of the allowlist, or which secret kinds were found —
	// and never contains a redacted value itself, only its Kind.
	Reasons []string
}

// Policy configures a Checker.
type Policy struct {
	// Secrets enables secret scanning (redact.Detect) over tool inputs,
	// tool results, messages, and file content.
	Secrets bool
	// HomePaths enables home-directory/username scrubbing
	// (redact.HomePaths) over tool inputs, tool results, and messages.
	// (Not applied to KindFile: a file whose content depends on the
	// operator's home directory is unusual enough, and rewriting file
	// bytes risky enough, that it's left alone here — Secrets in file
	// content still rejects as normal.)
	HomePaths bool
	// PathAllowlist restricts which file paths (KindFile) may be
	// captured at all. This fails closed: a nil PathAllowlist (the zero
	// Policy, or NewPathAllowlist's result on a non-git root) rejects
	// every KindFile blob rather than permitting them, since "we
	// couldn't determine what's safe to capture" is exactly the state a
	// privacy gate must not treat as "anything goes." A caller that
	// doesn't need file-path gating at all (e.g. checking only messages
	// and tool payloads) simply never passes KindFile to CheckBlob.
	PathAllowlist *PathAllowlist
	// Homes and Usernames are passed through to redact.HomePaths.
	Homes     []string
	Usernames []string
}

// Checker applies a Policy to individual blobs. Construct one with Gate.
type Checker struct {
	policy Policy
}

// Gate returns a Checker enforcing p.
func Gate(p Policy) *Checker {
	return &Checker{policy: p}
}

// CheckBlob decides what happens to one piece of captured content.
//
//   - kind is one of KindToolInput, KindToolResult, KindMessage, or
//     KindFile.
//   - path is the workspace-relative file path; only meaningful (and
//     only checked against the allowlist) for KindFile, ignored
//     otherwise.
//
// Rules, applied in order:
//
//  1. KindFile whose path is not in the policy's PathAllowlist -> Reject,
//     naming the path.
//  2. Content containing a detected secret (Policy.Secrets):
//     - for KindFile -> Reject. Files are never rewritten: the secret is
//     literally what's on disk in the user's tree, and silently
//     redacting the captured copy would desync it from the real file
//     without fixing the actual leak. The user has to fix the file and
//     let the next snapshot pick up the fix.
//     - for every other kind -> Rewrite, with the secret spans replaced
//     per redact.Redact.
//  3. Home-directory paths / usernames found in the content
//     (Policy.HomePaths, KindFile excluded — see Policy.HomePaths) ->
//     Rewrite, with them scrubbed. This composes with rule 2: a blob can
//     be rewritten for both secrets and home paths.
//  4. Otherwise -> Accept, with Content equal to the input.
func (c *Checker) CheckBlob(kind string, path string, content []byte) Decision {
	if kind == KindFile && !c.policy.PathAllowlist.Allowed(path) {
		return Decision{
			Action:  Reject,
			Reasons: []string{fmt.Sprintf("path %q is not in the public project's allowlist", path)},
		}
	}

	current := content
	var reasons []string
	rewritten := false

	if c.policy.Secrets {
		if findings := redact.Detect(current); len(findings) > 0 {
			if kind == KindFile {
				return Decision{
					Action: Reject,
					Reasons: []string{fmt.Sprintf(
						"file %q contains %s and cannot be captured for a public project; fix the file and re-push",
						path, findingKinds(findings),
					)},
				}
			}
			redacted, applied := redact.Redact(current, redact.Options{})
			current = redacted
			rewritten = true
			reasons = append(reasons, fmt.Sprintf("%s: redacted %s", kind, findingKinds(applied)))
		}
	}

	if c.policy.HomePaths && kind != KindFile {
		if scrubbed := redact.HomePaths(current, c.policy.Homes, c.policy.Usernames); !bytesEqual(scrubbed, current) {
			current = scrubbed
			rewritten = true
			reasons = append(reasons, fmt.Sprintf("%s: redacted home directory paths and/or usernames", kind))
		}
	}

	if !rewritten {
		return Decision{Action: Accept, Content: content}
	}
	return Decision{Action: Rewrite, Content: current, Reasons: reasons}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// findingKinds renders a de-duplicated, count-annotated, human-readable
// summary of finding kinds — e.g. "aws_access_key_id, github_token (x2)"
// — for use in Decision.Reasons. It never includes a finding's matched
// text or preview, only its Kind.
func findingKinds(findings []redact.Finding) string {
	counts := make(map[string]int, len(findings))
	var order []string
	for _, f := range findings {
		if counts[f.Kind] == 0 {
			order = append(order, f.Kind)
		}
		counts[f.Kind]++
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		if n := counts[k]; n > 1 {
			parts = append(parts, fmt.Sprintf("%s (x%d)", k, n))
		} else {
			parts = append(parts, k)
		}
	}
	return strings.Join(parts, ", ")
}
