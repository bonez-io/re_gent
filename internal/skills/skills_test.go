package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEverySkillHasTheFrontMatterAnAgentNeeds(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("no skills embedded")
	}
	for _, skill := range all {
		if skill.Description == "" {
			// The description is what an agent host matches a request against.
			// A skill without one is installed but never chosen.
			t.Errorf("%s: no description in front matter", skill.Name)
		}
		if skill.AllowedTools == "" {
			// The grant is the security-relevant half. A skill that declares
			// none cannot be shown to a user for approval.
			t.Errorf("%s: no allowed-tools in front matter", skill.Name)
		}
		if !strings.HasPrefix(skill.Content, "---") {
			t.Errorf("%s: does not begin with front matter", skill.Name)
		}
	}
}

// The repository installs its own skills into .claude/skills, and the binary
// ships them from internal/skills/data. Two copies drift: that is exactly how
// the shipped set fell to three skills while the repository carried nine. This
// test is the thing that notices.
func TestShippedSkillsMatchTheOnesTheRepositoryInstalls(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	for _, skill := range All() {
		installed := filepath.Join(repoRoot, ".claude", "skills", skill.Name, "SKILL.md")
		content, err := os.ReadFile(installed)
		if err != nil {
			t.Errorf("%s is shipped but not installed in .claude/skills: %v\n"+
				"run: rgt skill install --all", skill.Name, err)
			continue
		}
		if string(content) != skill.Content {
			t.Errorf("%s differs between internal/skills/data and .claude/skills.\n"+
				"internal/skills/data is canonical; run: rgt skill install %s --force", skill.Name, skill.Name)
		}
	}
}

func TestGetRejectsNamesThatCouldEscapeTheTree(t *testing.T) {
	for _, name := range []string{"", "..", "../../etc/passwd", "a/b", `a\b`, "blame/../blame", "Blame!"} {
		if _, err := Get(name); err == nil {
			t.Errorf("Get(%q) was accepted; it must be rejected", name)
		}
	}
}

func TestInstallWritesTheSkillAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	path, written, err := Install(dir, "blame", false)
	if err != nil || !written {
		t.Fatalf("first install: written=%v err=%v", written, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed skill: %v", err)
	}
	expected, _ := Get("blame")
	if string(content) != expected.Content {
		t.Error("installed content differs from the embedded skill")
	}

	// Re-installing an unchanged file is a no-op, not an error and not a rewrite.
	_, written, err = Install(dir, "blame", false)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if written {
		t.Error("an already-current skill was rewritten")
	}
}

// A user may edit a skill. Replacing their version without asking is quiet data
// loss, so it takes --force.
func TestInstallLeavesAnEditedSkillAloneUnlessForced(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Install(dir, "blame", false); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	path := filepath.Join(dir, "blame", "SKILL.md")
	if err := os.WriteFile(path, []byte("--- \nmine\n---\nedited by the user\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, written, err := Install(dir, "blame", false)
	if !IsExists(err) {
		t.Fatalf("expected the edited-file error, got written=%v err=%v", written, err)
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), "edited by the user") {
		t.Error("the user's edit was overwritten without --force")
	}

	if _, written, err = Install(dir, "blame", true); err != nil || !written {
		t.Fatalf("--force install: written=%v err=%v", written, err)
	}
	forced, _ := os.ReadFile(path)
	if strings.Contains(string(forced), "edited by the user") {
		t.Error("--force did not replace the edited file")
	}
}

func TestInstallRejectsAnUnknownSkill(t *testing.T) {
	if _, _, err := Install(t.TempDir(), "no-such-skill", false); err == nil {
		t.Error("installing an unknown skill was accepted")
	}
}
