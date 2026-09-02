package redact

import (
	"bytes"
	"sort"
	"strings"
)

// Finding describes one detected secret span within a piece of content.
type Finding struct {
	// Kind identifies the detector that matched, e.g. "aws_access_key_id",
	// "github_token", "jwt". See the detector table in detectors.go for the
	// full list.
	Kind string
	// Start and End are byte offsets into the scanned content, End
	// exclusive, delimiting exactly the text that should be redacted (the
	// secret value itself — for keyed assignments like `token=...` this is
	// the value, not the key).
	Start, End int
	// Preview is a masked rendering of the matched text, safe to log or
	// show to the user (e.g. "AKIA…XYZ"). It never contains the full
	// secret.
	Preview string
}

// Options configures Redact.
type Options struct {
	// Kinds restricts redaction to findings whose Kind is in this set. A
	// nil or empty slice means "redact every kind Detect can find".
	Kinds []string
}

// rawMatch is an internal, kind-tagged byte span produced by one detector
// before preview masking is computed.
type rawMatch struct {
	kind       string
	start, end int
}

// detectorFunc scans content and returns every span it believes is a
// secret of its kind. Detectors do not need to worry about overlaps with
// other detectors; Detect resolves those.
type detectorFunc func(content []byte) []rawMatch

// allDetectors is ordered from most to least specific. Detect walks them in
// order and, for each candidate match, skips it if it overlaps a span
// already accepted from an earlier (more specific) detector. This makes
// e.g. a Stripe key found inside a `token=...` assignment get reported as
// "stripe_key" rather than the generic "generic_secret_assignment".
var allDetectors = []detectorFunc{
	detectPrivateKeyPEM,
	detectAWSAccessKeyID,
	detectAWSSecretAccessKey,
	detectGitHubFineGrainedPAT,
	detectGitHubToken,
	detectGitLabPAT,
	detectSlackToken,
	detectStripeKey,
	detectGoogleAPIKey,
	detectAnthropicKey,
	detectOpenAIKey,
	detectRegentToken,
	detectJWT,
	detectDotEnvSecret,
	detectGenericSecretAssignment,
}

// Detect scans content and returns every secret it finds, sorted by Start,
// with overlapping matches from lower-priority detectors already resolved
// away (see allDetectors).
func Detect(content []byte) []Finding {
	var accepted []rawMatch
	for _, det := range allDetectors {
		for _, rm := range det(content) {
			if rm.start < 0 || rm.end <= rm.start || rm.end > len(content) {
				continue
			}
			if overlapsAny(accepted, rm) {
				continue
			}
			accepted = append(accepted, rm)
		}
	}

	sort.Slice(accepted, func(i, j int) bool { return accepted[i].start < accepted[j].start })

	findings := make([]Finding, 0, len(accepted))
	for _, rm := range accepted {
		findings = append(findings, Finding{
			Kind:    rm.kind,
			Start:   rm.start,
			End:     rm.end,
			Preview: maskPreview(content[rm.start:rm.end]),
		})
	}
	return findings
}

func overlapsAny(existing []rawMatch, m rawMatch) bool {
	for _, e := range existing {
		if m.start < e.end && e.start < m.end {
			return true
		}
	}
	return false
}

// Redact returns a copy of content with every detected finding replaced by
// the literal marker "[REDACTED:<Kind>]", along with the findings that were
// actually redacted (after filtering by opts.Kinds, if set). The returned
// findings' Start/End refer to offsets in the original content, not the
// output.
func Redact(content []byte, opts Options) ([]byte, []Finding) {
	findings := Detect(content)

	if len(opts.Kinds) > 0 {
		allowed := make(map[string]bool, len(opts.Kinds))
		for _, k := range opts.Kinds {
			allowed[k] = true
		}
		filtered := make([]Finding, 0, len(findings))
		for _, f := range findings {
			if allowed[f.Kind] {
				filtered = append(filtered, f)
			}
		}
		findings = filtered
	}

	if len(findings) == 0 {
		out := make([]byte, len(content))
		copy(out, content)
		return out, findings
	}

	var buf bytes.Buffer
	buf.Grow(len(content))
	last := 0
	for _, f := range findings {
		buf.Write(content[last:f.Start])
		buf.WriteString("[REDACTED:")
		buf.WriteString(f.Kind)
		buf.WriteString("]")
		last = f.End
	}
	buf.Write(content[last:])
	return buf.Bytes(), findings
}

// maskPreview renders a short, safe-to-display preview of a matched
// secret: for short values it is fully masked; for longer ones it keeps a
// small prefix and suffix, e.g. "AKIA…XYZ".
func maskPreview(secret []byte) string {
	r := []rune(string(secret))
	n := len(r)
	if n <= 6 {
		return strings.Repeat("*", n)
	}
	const prefixLen, suffixLen = 4, 3
	if n < prefixLen+suffixLen+1 {
		return string(r[:1]) + "…" + string(r[n-1:])
	}
	return string(r[:prefixLen]) + "…" + string(r[n-suffixLen:])
}
