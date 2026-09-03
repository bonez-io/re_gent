package pipeline

import (
	"regexp"
	"sort"
	"strings"
)

// Deterministic entities exist whether or not a model ever runs (RFC 0007
// decision 7): every URL in a turn, and every commit or branch a git
// command reported.

var (
	urlPattern = regexp.MustCompile(`https?://[^\s<>()\[\]"'` + "`" + `]+`)
	// A GitHub or GitLab pull request / issue path.
	githubPull   = regexp.MustCompile(`^https?://github\.com/([^/]+/[^/]+)/pull/(\d+)`)
	githubIssue  = regexp.MustCompile(`^https?://github\.com/([^/]+/[^/]+)/issues/(\d+)`)
	gitlabMerge  = regexp.MustCompile(`^https?://gitlab\.com/(.+?)/-/merge_requests/(\d+)`)
	gitlabIssue  = regexp.MustCompile(`^https?://gitlab\.com/(.+?)/-/issues/(\d+)`)
	linearIssue  = regexp.MustCompile(`^https?://linear\.app/[^/]+/issue/([A-Z0-9]+-\d+)`)
	jiraIssue    = regexp.MustCompile(`^https?://[^/]+\.atlassian\.net/browse/([A-Z0-9]+-\d+)`)
	commitOutput = regexp.MustCompile(`(?m)^\[[^\]\s]+(?: \([^)]*\))? ([0-9a-f]{7,40})\]|^commit ([0-9a-f]{40})\b`)
	branchCreate = regexp.MustCompile(`git\s+(?:checkout\s+-b|switch\s+-c|branch)\s+([A-Za-z0-9._/-]+)`)
	branchPush   = regexp.MustCompile(`git\s+push\s+(?:-u\s+|--set-upstream\s+)?[A-Za-z0-9._-]+\s+([A-Za-z0-9._/-]+)`)
)

// entityKey dedupes on (type, ref) when ref is set, else (type, lower(name)).
func entityKey(e EntityView) string {
	if e.Ref != "" {
		return e.Type + "\x00" + e.Ref
	}
	return e.Type + "\x00\x00" + strings.ToLower(e.Name)
}

// extractURLs returns every URL in text as an entity with the given evidence.
func extractURLs(text, evidence string) []EntityView {
	var out []EntityView
	for _, raw := range urlPattern.FindAllString(text, -1) {
		u := strings.TrimRight(raw, ".,;:!?")
		// A closing paren or bracket is usually markdown around the URL,
		// unless the URL itself opened one.
		for strings.HasSuffix(u, ")") && strings.Count(u, "(") < strings.Count(u, ")") {
			u = strings.TrimSuffix(u, ")")
		}
		if u == "" {
			continue
		}
		typ, name := classifyURL(u)
		out = append(out, EntityView{Type: typ, Name: name, Ref: u, EvidenceStepID: evidence})
	}
	return out
}

func classifyURL(u string) (typ, name string) {
	switch {
	case githubPull.MatchString(u):
		m := githubPull.FindStringSubmatch(u)
		return "pull_request", m[1] + "#" + m[2]
	case githubIssue.MatchString(u):
		m := githubIssue.FindStringSubmatch(u)
		return "issue", m[1] + "#" + m[2]
	case gitlabMerge.MatchString(u):
		m := gitlabMerge.FindStringSubmatch(u)
		return "merge_request", m[1] + "!" + m[2]
	case gitlabIssue.MatchString(u):
		m := gitlabIssue.FindStringSubmatch(u)
		return "issue", m[1] + "#" + m[2]
	case linearIssue.MatchString(u):
		return "issue", linearIssue.FindStringSubmatch(u)[1]
	case jiraIssue.MatchString(u):
		return "issue", jiraIssue.FindStringSubmatch(u)[1]
	}
	name = u
	if i := strings.Index(name, "://"); i >= 0 {
		name = name[i+3:]
	}
	if len(name) > 80 {
		name = name[:77] + "…"
	}
	return "link", name
}

// extractGit returns commits and branches a git command reported. command is
// the shell command, output what it printed.
func extractGit(command, output, evidence string) []EntityView {
	if !strings.Contains(command, "git ") && !strings.HasPrefix(strings.TrimSpace(command), "git") {
		return nil
	}
	var out []EntityView
	for _, m := range commitOutput.FindAllStringSubmatch(output, -1) {
		hash := m[1]
		if hash == "" {
			hash = m[2]
		}
		if hash == "" {
			continue
		}
		short := hash
		if len(short) > 12 {
			short = short[:12]
		}
		out = append(out, EntityView{Type: "commit", Name: short, Ref: hash, EvidenceStepID: evidence})
	}
	for _, re := range []*regexp.Regexp{branchCreate, branchPush} {
		for _, m := range re.FindAllStringSubmatch(command, -1) {
			name := m[1]
			if name == "" || name == "HEAD" || strings.HasPrefix(name, "-") {
				continue
			}
			out = append(out, EntityView{Type: "branch", Name: name, Ref: "", EvidenceStepID: evidence})
		}
	}
	return out
}

// dedupeEntities keeps the first occurrence of each entity key, in a stable
// order by type then name.
func dedupeEntities(in []EntityView) []EntityView {
	seen := map[string]bool{}
	var out []EntityView
	for _, e := range in {
		k := entityKey(e)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out
}
