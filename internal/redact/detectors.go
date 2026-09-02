package redact

import (
	"bytes"
	"encoding/base64"
	"math"
	"regexp"
	"strings"
)

// This file holds the baseline detector set. Each detector is documented
// with what it matches and why, and has a corresponding test vector in
// redact_test.go (search for the same Kind string).
//
// Performance note: most of these tokens have a short fixed literal prefix
// (AKIA, ghp_, glpat-, ...). Running the full regex over a multi-megabyte
// blob just to conclude "not present" is wasteful, so each such detector
// first does a cheap literal bytes.Contains check (an order of magnitude
// faster than the regex engine) and only invokes its regexp when at least
// one candidate prefix is actually present. This keeps Detect fast on the
// common case — a blob that contains at most a couple of kinds of secret,
// if any — without changing what gets matched.

func containsAny(content []byte, literals ...string) bool {
	for _, l := range literals {
		if bytes.Contains(content, []byte(l)) {
			return true
		}
	}
	return false
}

// --- AWS -------------------------------------------------------------

// AWS access key IDs are a fixed-format 20-character token: a two-to-four
// letter type prefix (AKIA for long-term user keys, ASIA for STS temporary
// credentials, etc.) followed by 16 uppercase-alnum characters. The prefix
// list is the one AWS documents; \b keeps us from matching the ID out of a
// longer alnum run.
//
// Example: AKIAIOSFODNN7EXAMPLE
var awsAccessKeyIDRe = regexp.MustCompile(`\b(?:AKIA|ABIA|ACCA|ASIA|AROA|AIDA|AGPA|AIPA|ANPA|ANVA)[0-9A-Z]{16}\b`)

func detectAWSAccessKeyID(content []byte) []rawMatch {
	if !containsAny(content, "AKIA", "ABIA", "ACCA", "ASIA", "AROA", "AIDA", "AGPA", "AIPA", "ANPA", "ANVA") {
		return nil
	}
	return simpleMatches("aws_access_key_id", awsAccessKeyIDRe, content)
}

// AWS secret access keys are 40 characters of base64-alphabet with no fixed
// prefix, so matching them unconditionally would be far too noisy (any 40
// random-looking chars would hit). Instead we require the conventional
// `aws_secret_access_key = <value>` assignment as context, mirroring how
// these are actually written in .env/.aws/config files and shell exports.
//
// Example: aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
var awsSecretAccessKeyRe = regexp.MustCompile(`(?i)aws_secret_access_key\s*[:=]\s*['"]?([A-Za-z0-9/+=]{40})['"]?`)

func detectAWSSecretAccessKey(content []byte) []rawMatch {
	// The regex is already (?i), but the literal gate isn't; check both the
	// conventional lower_snake and UPPER_SNAKE spellings the key is almost
	// always written in.
	if !containsAny(content, "aws_secret_access_key", "AWS_SECRET_ACCESS_KEY") {
		return nil
	}
	return submatchMatches("aws_secret_access_key", awsSecretAccessKeyRe, 1, content)
}

// --- GitHub ------------------------------------------------------------

// Classic GitHub tokens: gh + one of p(ersonal)/o(auth)/u(ser-to-server)/
// s(erver-to-server)/r(efresh) + underscore + 36 base62 characters.
//
// Example: ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ
var githubTokenRe = regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36}\b`)

func detectGitHubToken(content []byte) []rawMatch {
	if !containsAny(content, "ghp_", "gho_", "ghu_", "ghs_", "ghr_") {
		return nil
	}
	return simpleMatches("github_token", githubTokenRe, content)
}

// Fine-grained GitHub personal access tokens: github_pat_ + 22 chars +
// underscore + 59 chars.
//
// Example: github_pat_abcdefghijklmnopqrstuv_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456
var githubFineGrainedPATRe = regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9]{22}_[A-Za-z0-9]{59}\b`)

func detectGitHubFineGrainedPAT(content []byte) []rawMatch {
	if !containsAny(content, "github_pat_") {
		return nil
	}
	return simpleMatches("github_pat", githubFineGrainedPATRe, content)
}

// --- GitLab --------------------------------------------------------------

// GitLab personal access tokens: glpat- + 20 base62/underscore/hyphen chars.
//
// Example: glpat-abcdefghijklmnopqrst
var gitlabPATRe = regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20}\b`)

func detectGitLabPAT(content []byte) []rawMatch {
	if !containsAny(content, "glpat-") {
		return nil
	}
	return simpleMatches("gitlab_pat", gitlabPATRe, content)
}

// --- Slack ---------------------------------------------------------------

// Slack tokens: xox + one of a(pp)/b(ot)/p(ortal - legacy user)/r(efresh)
// + hyphen-separated segments of digits/letters, commonly
// xoxb-<team>-<id>-<secret>.
//
// Shape: xoxb-<team digits>-<id digits>-<24 base62 characters>. No literal
// example here on purpose: full-format tokens trip push protection.
var slackTokenRe = regexp.MustCompile(`\bxox[abpr]-[0-9A-Za-z-]{10,72}\b`)

func detectSlackToken(content []byte) []rawMatch {
	if !containsAny(content, "xoxa-", "xoxb-", "xoxp-", "xoxr-") {
		return nil
	}
	return simpleMatches("slack_token", slackTokenRe, content)
}

// --- Stripe ----------------------------------------------------------

// Stripe secret/restricted keys: sk_live_/sk_test_/rk_live_ followed by
// 24+ base62 characters. (Publishable keys, pk_*, are not secret and are
// intentionally not matched.)
//
// Shape: sk_live_ followed by 24+ base62 characters. No literal example here
// on purpose: full-format keys trip push protection.
var stripeKeyRe = regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{24,}\b`)

func detectStripeKey(content []byte) []rawMatch {
	if !containsAny(content, "sk_live_", "sk_test_", "rk_live_", "rk_test_") {
		return nil
	}
	return simpleMatches("stripe_key", stripeKeyRe, content)
}

// --- Google ----------------------------------------------------------

// Google API keys: AIza + 35 URL-safe base64 characters (fixed 39-char
// total length in practice).
//
// Example: AIzaabcdefghijklmnopqrstuvwxyzABCDEFGHI
var googleAPIKeyRe = regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)

func detectGoogleAPIKey(content []byte) []rawMatch {
	if !containsAny(content, "AIza") {
		return nil
	}
	return simpleMatches("google_api_key", googleAPIKeyRe, content)
}

// --- Anthropic / OpenAI ----------------------------------------------

// Anthropic API keys: sk-ant-, optionally followed by an api version
// segment like api03-, then 20+ base62/hyphen/underscore characters.
// Checked before the generic OpenAI pattern so "sk-ant-..." is reported as
// anthropic_api_key, not openai_api_key.
//
// Example: sk-ant-api03-abcdefghijklmnopqrstuvwx
var anthropicKeyRe = regexp.MustCompile(`\bsk-ant-(?:api\d{2}-)?[A-Za-z0-9_-]{20,}\b`)

func detectAnthropicKey(content []byte) []rawMatch {
	if !containsAny(content, "sk-ant-") {
		return nil
	}
	return simpleMatches("anthropic_api_key", anthropicKeyRe, content)
}

// OpenAI API keys: sk-, optionally proj-, then 20+ alnum characters. Uses a
// hyphen after "sk" (unlike Stripe's "sk_"), which is what distinguishes
// it; the Anthropic detector runs first and claims "sk-ant-..." so this
// pattern only ever gets to report the remainder.
//
// Example: sk-abcdefghijklmnopqrstuvwx
var openaiKeyRe = regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9]{20,}\b`)

func detectOpenAIKey(content []byte) []rawMatch {
	if !containsAny(content, "sk-") {
		return nil
	}
	return simpleMatches("openai_api_key", openaiKeyRe, content)
}

// --- re_gent's own tokens ---------------------------------------------

// re_gent-issued personal-access/service/session tokens.
//
// Example: rgt_pat_abcdefghijklmnopqrstuvwx
var regentTokenRe = regexp.MustCompile(`\brgt_(?:pat|svc|session)_[A-Za-z0-9]{20,}\b`)

func detectRegentToken(content []byte) []rawMatch {
	if !containsAny(content, "rgt_pat_", "rgt_svc_", "rgt_session_") {
		return nil
	}
	return simpleMatches("regent_token", regentTokenRe, content)
}

// --- PEM private keys --------------------------------------------------

// PEM-encoded private key blocks. Go's RE2 engine has no backreferences,
// so the BEGIN/END labels aren't required to match each other exactly
// (e.g. "BEGIN RSA PRIVATE KEY" / "END PRIVATE KEY" would still match);
// that's an acceptable false-negative-avoidance trade for a detector whose
// whole point is "there is a private key here at all".
//
// Example:
//
//	-----BEGIN RSA PRIVATE KEY-----
//	MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu
//	-----END RSA PRIVATE KEY-----
var privateKeyPEMRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)

func detectPrivateKeyPEM(content []byte) []rawMatch {
	if !containsAny(content, "-----BEGIN ") {
		return nil
	}
	return simpleMatches("private_key_pem", privateKeyPEMRe, content)
}

// --- JWT -----------------------------------------------------------------

// JWTs are three base64url segments separated by dots. That shape alone is
// too easily confused with e.g. version-ish dotted tokens, so after the
// loose shape matches we base64url-decode the first segment and require it
// to actually look like a JWT header (starts with `{"alg"`), which is
// nearly impossible to hit by accident.
//
// Example header/payload built from {"alg":"HS256","typ":"JWT"} and
// {"sub":"1234567890","name":"Test"}.
var jwtShapeRe = regexp.MustCompile(`\b[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)

func detectJWT(content []byte) []rawMatch {
	// Virtually every real JWT starts with "eyJ": it's the base64url
	// encoding of `{"` (the start of the header JSON), which is universal
	// because the header is always a JSON object. This lets us skip the
	// (comparatively expensive, due to the repeated wide char classes)
	// shape regex entirely on content with no JWT-shaped token at all.
	if !containsAny(content, "eyJ") {
		return nil
	}
	idxs := jwtShapeRe.FindAllIndex(content, -1)
	var out []rawMatch
	for _, m := range idxs {
		if looksLikeJWT(content[m[0]:m[1]]) {
			out = append(out, rawMatch{kind: "jwt", start: m[0], end: m[1]})
		}
	}
	return out
}

func looksLikeJWT(token []byte) bool {
	parts := bytes.SplitN(token, []byte("."), 3)
	if len(parts) != 3 {
		return false
	}
	header, err := base64URLDecode(parts[0])
	if err != nil {
		return false
	}
	header = bytes.TrimSpace(header)
	return bytes.HasPrefix(header, []byte(`{"alg"`))
}

func base64URLDecode(s []byte) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(string(s)); err == nil {
		return b, nil
	}
	// Fall back to padded decoding in case the segment happened to include
	// padding characters (non-standard but harmless to accept).
	padded := string(s)
	if m := len(padded) % 4; m != 0 {
		padded += strings.Repeat("=", 4-m)
	}
	return base64.URLEncoding.DecodeString(padded)
}

// --- .env-style KEY=value ------------------------------------------------

// Matches whole-line KEY=value assignments (shell/.env style) and reports
// only those whose key mentions secret/token/password/api_key/private,
// case-insensitively — the convention this project's own docs (see
// CLAUDE.md's baseline detector list) call out explicitly.
//
// Example: DB_PASSWORD=Sup3rSecretValue123
var dotEnvLineRe = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*(.+?)[ \t]*$`)
var dotEnvKeyRe = regexp.MustCompile(`(?i)secret|token|password|api_key|private`)

func detectDotEnvSecret(content []byte) []rawMatch {
	if !containsAny(content, "secret", "Secret", "SECRET", "token", "Token", "TOKEN",
		"password", "Password", "PASSWORD", "api_key", "API_KEY", "private", "Private", "PRIVATE") {
		return nil
	}
	ms := dotEnvLineRe.FindAllSubmatchIndex(content, -1)
	var out []rawMatch
	for _, m := range ms {
		ks, ke := m[2], m[3]
		vs, ve := m[4], m[5]
		if !dotEnvKeyRe.Match(content[ks:ke]) {
			continue
		}
		value := content[vs:ve]
		if len(value) < 8 || !looksLikeSecretValue(value) {
			continue
		}
		out = append(out, rawMatch{kind: "dotenv_secret", start: vs, end: ve})
	}
	return out
}

// --- generic password=/secret=/token= assignments -------------------

// Catches free-form assignments (shell exports, JSON-ish "key": "value",
// prose like `token = "..."`) that name password/secret/token directly,
// with a value of at least 8 characters. This is intentionally broader
// than detectDotEnvSecret (no whole-line anchor, no api_key/private
// keys) and runs last so any of the more specific detectors above claim
// the match first.
//
// Example: password = "Tr0ub4dor&3xyz"
var genericSecretAssignmentRe = regexp.MustCompile(`(?i)\b(?:password|secret|token)\s*[:=]\s*['"]?([^\s'"]{8,})['"]?`)

func detectGenericSecretAssignment(content []byte) []rawMatch {
	if !containsAny(content, "password", "Password", "PASSWORD", "secret", "Secret", "SECRET",
		"token", "Token", "TOKEN") {
		return nil
	}
	ms := genericSecretAssignmentRe.FindAllSubmatchIndex(content, -1)
	var out []rawMatch
	for _, m := range ms {
		vs, ve := m[2], m[3]
		value := content[vs:ve]
		if !looksLikeSecretValue(value) {
			continue
		}
		out = append(out, rawMatch{kind: "generic_secret_assignment", start: vs, end: ve})
	}
	return out
}

// --- shared helpers ------------------------------------------------------

func simpleMatches(kind string, re *regexp.Regexp, content []byte) []rawMatch {
	idxs := re.FindAllIndex(content, -1)
	out := make([]rawMatch, 0, len(idxs))
	for _, m := range idxs {
		out = append(out, rawMatch{kind: kind, start: m[0], end: m[1]})
	}
	return out
}

// submatchMatches reports the span of capture group `group` (1-based) from
// each match of re, rather than the whole match.
func submatchMatches(kind string, re *regexp.Regexp, group int, content []byte) []rawMatch {
	ms := re.FindAllSubmatchIndex(content, -1)
	out := make([]rawMatch, 0, len(ms))
	for _, m := range ms {
		s, e := m[2*group], m[2*group+1]
		if s < 0 || e < 0 {
			continue
		}
		out = append(out, rawMatch{kind: kind, start: s, end: e})
	}
	return out
}

// placeholderValues are common non-secret filler values that would
// otherwise trip the generic/dotenv assignment detectors, keeping the
// false-positive rate down on example configs and docs.
var placeholderValues = map[string]bool{
	"changeme": true, "change_me": true, "change-me": true,
	"placeholder": true, "example": true, "redacted": true,
	"<redacted>": true, "yourpassword": true, "yoursecret": true,
	"yourtoken": true, "your_password_here": true, "xxxxxxxx": true,
	"todo": true, "tbd": true, "null": true, "none": true, "undefined": true,
}

// looksLikeSecretValue filters out obvious placeholders and low-entropy
// filler (repeated characters, short common words) from the generic and
// dotenv assignment detectors, which otherwise have no format constraint
// to lean on the way the prefixed-token detectors do.
func looksLikeSecretValue(value []byte) bool {
	s := string(value)
	if placeholderValues[strings.ToLower(s)] {
		return false
	}
	if isRepeatedRune(s) {
		return false
	}
	return shannonEntropy(s) >= 2.0
}

func isRepeatedRune(s string) bool {
	var first rune
	for i, r := range s {
		if i == 0 {
			first = r
			continue
		}
		if r != first {
			return false
		}
	}
	return true
}

// shannonEntropy returns the Shannon entropy of s in bits per character.
func shannonEntropy(s string) float64 {
	if len(s) == 0 {
		return 0
	}
	counts := make(map[rune]int)
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	total := float64(n)
	var entropy float64
	for _, c := range counts {
		p := float64(c) / total
		entropy -= p * math.Log2(p)
	}
	return entropy
}
