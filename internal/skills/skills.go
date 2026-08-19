// Package skills is the single source of truth for the agent skills re_gent
// ships.
//
// A skill is a prompt plus a tool grant: a SKILL.md an agent host loads at
// startup, whose front matter declares what the skill may run. The analysis is
// the agent's, the data is re_gent's, and the skill is the wiring between them.
//
// The definitions live as real files under data/ and are embedded into the
// binary, so `rgt init` and `rgt skill install` hand out the same bytes that
// are reviewed in the repository. They used to be Go string literals inside
// init.go, which is how the shipped set silently drifted to three skills while
// the repository carried nine.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed all:data
var files embed.FS

const root = "data"

// Skill is one installable skill.
type Skill struct {
	Name string
	// Description is the front-matter `description` line: what an agent host
	// matches a user's request against.
	Description string
	// AllowedTools is the front-matter `allowed-tools` line. It is the tool
	// grant, so callers that install a skill should be able to show it.
	AllowedTools string
	Content      string
}

// notShipped lists skills that exist in the repository but are withheld from
// `rgt init` and `--all`, with the reason.
//
// A skill is a promise about what a command does. Shipping one that overstates
// the command teaches the agent to offer something that will not happen, which
// is worse than not shipping it.
var notShipped = map[string]string{
	// The skill says it restores "workspace and conversation" and writes a
	// backup to .regent/backups/. `rgt rewind` restores the workspace only and
	// prints an undo command. Ship it once the text matches the command, or
	// once conversation rewind exists.
	"rewind": "describes conversation rewind, which rgt rewind does not do",
}

// Bootstrap is the skill that teaches an agent to find and install the others.
// `rgt init` writes it unconditionally: it is the entry point to the catalog,
// and an entry point behind a flag is one nobody finds.
const Bootstrap = "regent-skills"

// Withheld returns the reason a skill is not shipped by default, or "" if it is.
func Withheld(name string) string { return notShipped[name] }

// DefaultNames returns the skills `rgt init` installs: everything embedded,
// minus the ones withheld above.
func DefaultNames() []string {
	var names []string
	for _, name := range Names() {
		if _, withheld := notShipped[name]; !withheld {
			names = append(names, name)
		}
	}
	return names
}

// Names returns every embedded skill name, sorted.
func Names() []string {
	entries, err := fs.ReadDir(files, root)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

// Get returns one skill by name.
func Get(name string) (Skill, error) {
	if !validName(name) {
		return Skill{}, fmt.Errorf("unknown skill %q", name)
	}
	content, err := files.ReadFile(path(name))
	if err != nil {
		return Skill{}, fmt.Errorf("unknown skill %q", name)
	}
	text := string(content)
	return Skill{
		Name:         name,
		Description:  frontMatter(text, "description"),
		AllowedTools: frontMatter(text, "allowed-tools"),
		Content:      text,
	}, nil
}

// All returns every embedded skill, sorted by name.
func All() []Skill {
	names := Names()
	all := make([]Skill, 0, len(names))
	for _, name := range names {
		skill, err := Get(name)
		if err != nil {
			continue
		}
		all = append(all, skill)
	}
	return all
}

// Install writes a skill into skillsDir as <name>/SKILL.md and reports the path
// it wrote.
//
// An existing file is left alone unless overwrite is set: a user may have
// edited a skill, and silently replacing their version is the kind of quiet
// data loss an install command must not do. The caller is told which case it
// was so it can say so.
func Install(skillsDir, name string, overwrite bool) (path string, written bool, err error) {
	skill, err := Get(name)
	if err != nil {
		return "", false, err
	}
	return InstallContent(skillsDir, skill.Name, skill.Content, overwrite)
}

// InstallContent writes a skill whose text came from somewhere other than the
// embedded set — a server's registry, say — under the same rules as Install.
//
// The write rules belong here rather than at each call site: whether a skill
// came from this binary or from a server, the user's edited copy outranks it.
func InstallContent(skillsDir, name, content string, overwrite bool) (path string, written bool, err error) {
	if !validName(name) {
		return "", false, fmt.Errorf("invalid skill name %q", name)
	}
	dir := filepath.Join(skillsDir, name)
	target := filepath.Join(dir, "SKILL.md")

	if !overwrite {
		if existing, statErr := os.ReadFile(target); statErr == nil {
			if string(existing) == content {
				return target, false, nil // already current; nothing to do
			}
			return target, false, errExists{target}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return "", false, fmt.Errorf("write %s: %w", target, err)
	}
	return target, true, nil
}

// errExists reports a skill file that differs from the shipped one and was left
// alone. It is a distinct type so callers can offer --force rather than
// printing a generic failure.
type errExists struct{ path string }

func (e errExists) Error() string {
	return fmt.Sprintf("%s already exists and differs from the shipped version", e.path)
}

// IsExists reports whether err is the "left an edited skill alone" case.
func IsExists(err error) bool {
	_, ok := err.(errExists)
	return ok
}

func path(name string) string { return root + "/" + name + "/SKILL.md" }

// validName rejects anything that could escape the embedded tree. Names come
// from user input on the command line.
func validName(name string) bool {
	if name == "" || strings.ContainsAny(name, `/\.`) {
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

// frontMatter reads one `key: value` line out of the YAML front matter. The
// front matter is a handful of flat keys, so a full YAML dependency would buy
// nothing here.
func frontMatter(text, key string) string {
	if !strings.HasPrefix(text, "---") {
		return ""
	}
	rest := text[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		trimmed := strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(trimmed, key+":"); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}
