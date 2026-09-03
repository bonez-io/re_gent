package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bonez-io/re_gent/internal/skills"
)

// The skills registry.
//
//	GET /api/skills          the catalog
//	GET /api/skills/<name>   one skill's SKILL.md, as text
//
// It is deliberately *not* repo-scoped. A skill describes how to interrogate
// re_gent, not one project's history, and storing a copy per repository would
// make "install bug-blame" mean something different in each one.
//
// The catalog is the binary's embedded skills, overlaid by an optional
// directory the operator points at. The embedded set means a fresh server is
// useful with no setup; the directory is what lets a team publish a skill of
// their own without waiting for a release.

// skillJSON is one catalog entry. The tool grant is included because a client
// should be able to show what a skill may run *before* anyone installs it.
type skillJSON struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	AllowedTools string `json:"allowed_tools,omitempty"`
	ArgumentHint string `json:"argument_hint,omitempty"`
	Source       string `json:"source"` // "builtin" or "local"
	Withheld     string `json:"withheld,omitempty"`
}

type skillsResponse struct {
	Total  int         `json:"total"`
	Skills []skillJSON `json:"skills"`
}

// handleSkills routes the registry. Only GET: the registry is read-only over
// HTTP, so a compromised client cannot publish a skill — and a skill is
// executable instruction, which makes that the difference that matters.
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request, segs []string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch len(segs) {
	case 2:
		s.handleSkillList(w)
	case 3:
		s.handleSkillContent(w, segs[2])
	default:
		httpError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) handleSkillList(w http.ResponseWriter) {
	catalog := map[string]skillJSON{}

	for _, skill := range skills.All() {
		catalog[skill.Name] = skillJSON{
			Name:         skill.Name,
			Description:  skill.Description,
			AllowedTools: skill.AllowedTools,
			ArgumentHint: frontMatterValue(skill.Content, "argument-hint"),
			Source:       "builtin",
			Withheld:     skills.Withheld(skill.Name),
		}
	}
	// A locally published skill of the same name replaces the built-in one:
	// the operator who put a file on this server is the more specific answer.
	for name, content := range s.localSkills() {
		catalog[name] = skillJSON{
			Name:         name,
			Description:  frontMatterValue(content, "description"),
			AllowedTools: frontMatterValue(content, "allowed-tools"),
			ArgumentHint: frontMatterValue(content, "argument-hint"),
			Source:       "local",
		}
	}

	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)

	out := skillsResponse{Total: len(names), Skills: make([]skillJSON, 0, len(names))}
	for _, name := range names {
		out.Skills = append(out.Skills, catalog[name])
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSkillContent(w http.ResponseWriter, name string) {
	if !validSkillName(name) {
		httpError(w, http.StatusBadRequest, "invalid skill name")
		return
	}
	if content, ok := s.localSkills()[name]; ok {
		writeSkill(w, content)
		return
	}
	skill, err := skills.Get(name)
	if err != nil {
		httpError(w, http.StatusNotFound, "unknown skill "+name)
		return
	}
	writeSkill(w, skill.Content)
}

func writeSkill(w http.ResponseWriter, content string) {
	// text/markdown so a browser shows it rather than downloading it, and so
	// `curl | rgt skill install -` stays plausible later.
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(content))
}

// localSkills reads the operator's published skills, if a directory was
// configured. Read on each request rather than cached: a registry an operator
// has to restart to publish to is one they will stop using, and the directory
// is small.
func (s *Server) localSkills() map[string]string {
	if s.skillsDir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.skillsDir)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() || !validSkillName(entry.Name()) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(s.skillsDir, entry.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		out[entry.Name()] = string(content)
	}
	return out
}

// validSkillName mirrors internal/skills: a name is a path segment supplied by
// a caller, so it must not be able to leave the directory.
func validSkillName(name string) bool {
	if name == "" || len(name) > 64 || strings.ContainsAny(name, `/\.`) {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// frontMatterValue reads one key out of a SKILL.md's YAML front matter. The
// front matter is a handful of flat keys, so a YAML dependency would buy
// nothing.
func frontMatterValue(text, key string) string {
	if !strings.HasPrefix(text, "---") {
		return ""
	}
	rest := text[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), key+":"); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}
