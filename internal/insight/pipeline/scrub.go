package pipeline

import (
	"fmt"
	"os"
	"os/user"
	"regexp"

	"github.com/bonez-io/re_gent/internal/redact"
)

// Scrubber rewrites bytes before they leave the machine: the full detector
// set of internal/redact, home directories and usernames, and the
// repository's own patterns. It runs unconditionally on every provider
// request (RFC 0007 decision 4); a local provider is scrubbed the same way,
// because the cost is nil and the alternative is a setting nobody audits.
type Scrubber struct {
	patterns  []*regexp.Regexp
	homes     []string
	usernames []string
}

// NewScrubber compiles the repository's patterns. A pattern that does not
// compile is an error, not a silent skip: a scrub policy that half-applies
// is worse than one that refuses to run.
func NewScrubber(patterns []string) (*Scrubber, error) {
	s := &Scrubber{}
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("[insight.scrub] pattern %q: %w", p, err)
		}
		s.patterns = append(s.patterns, re)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s.homes = append(s.homes, home)
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		s.usernames = append(s.usernames, u.Username)
	}
	return s, nil
}

// Scrub returns the scrubbed copy of content.
func (s *Scrubber) Scrub(content []byte) []byte {
	out, _ := redact.Redact(content, redact.Options{})
	out = redact.HomePaths(out, s.homes, s.usernames)
	for _, re := range s.patterns {
		out = re.ReplaceAll(out, []byte("[REDACTED:pattern]"))
	}
	return out
}

// ScrubString is Scrub for strings.
func (s *Scrubber) ScrubString(text string) string {
	if text == "" {
		return ""
	}
	return string(s.Scrub([]byte(text)))
}
