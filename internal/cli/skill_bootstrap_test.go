package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regent-vcs/regent/internal/skills"
)

// The bootstrap skill is the entry point to the catalog. If it were opt-in like
// the rest, a project would hold a catalog that nothing inside the agent knows
// to look at — which is the state this whole feature exists to fix.
func TestBootstrapSkillIsInstalledWithoutTheSkillsFlag(t *testing.T) {
	root := t.TempDir()
	if err := installBootstrapSkill(root, []agentTarget{agentClaude}); err != nil {
		t.Fatalf("install bootstrap: %v", err)
	}
	path := filepath.Join(root, ".claude", "skills", skills.Bootstrap, "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("bootstrap skill not installed: %v", err)
	}
}

// Only the bootstrap. The rest stay opt-in, so a plain init must not quietly
// write nine files into someone's repository.
func TestBootstrapInstallWritesNothingElse(t *testing.T) {
	root := t.TempDir()
	if err := installBootstrapSkill(root, []agentTarget{agentClaude}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".claude", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != skills.Bootstrap {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("installed %v, want only %s", names, skills.Bootstrap)
	}
}

// A skill written where the host does not read it is a file that does nothing.
func TestBootstrapFollowsTheHostsThatWereActuallyWired(t *testing.T) {
	root := t.TempDir()
	if err := installBootstrapSkill(root, []agentTarget{agentClaude, agentCodex}); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{".claude", ".agents"} {
		if _, err := os.Stat(filepath.Join(root, dir, "skills", skills.Bootstrap, "SKILL.md")); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}
	// A host that was not wired gets nothing.
	if _, err := os.Stat(filepath.Join(root, ".opencode")); !os.IsNotExist(err) {
		t.Error(".opencode was written to, but OpenCode was not wired")
	}
}

func TestBootstrapWritesNothingWhenNoHostWasWired(t *testing.T) {
	root := t.TempDir()
	if err := installBootstrapSkill(root, nil); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(root); err == nil && len(entries) != 0 {
		t.Errorf("wrote %d entries with no host wired", len(entries))
	}
}

// It is installed on every project by default, so its grant is the one that
// most needs to stay narrow. Discovery and installation only — never a bare
// shell, never anything that can push or rewind.
func TestBootstrapSkillGrantsOnlySkillCommands(t *testing.T) {
	skill, err := skills.Get(skills.Bootstrap)
	if err != nil {
		t.Fatalf("bootstrap skill is not embedded: %v", err)
	}
	grant := skill.AllowedTools
	if grant == "" {
		t.Fatal("bootstrap skill declares no allowed-tools")
	}
	for _, forbidden := range []string{"Bash(*)", "rgt push", "rgt rewind", "rgt connect", "Write", "Edit"} {
		if strings.Contains(grant, forbidden) {
			t.Errorf("grant includes %q: %s", forbidden, grant)
		}
	}
	if !strings.Contains(grant, "rgt skill") {
		t.Errorf("grant does not allow the skill commands it documents: %s", grant)
	}
}

// The skill tells an agent what to run. If it names a command that does not
// exist, it fails at the moment someone trusts it.
func TestBootstrapSkillOnlyNamesRealCommands(t *testing.T) {
	skill, err := skills.Get(skills.Bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	skillCmd := SkillCmd()
	install, _, err := skillCmd.Find([]string{"install"})
	if err != nil {
		t.Fatalf("rgt skill install not found: %v", err)
	}
	if _, _, err := skillCmd.Find([]string{"list"}); err != nil {
		t.Errorf("bootstrap skill documents `rgt skill list`, which does not exist: %v", err)
	}
	for _, flag := range []string{"agent", "force", "server"} {
		if install.Flags().Lookup(flag) == nil {
			t.Errorf("bootstrap skill documents --%s, which rgt skill install does not have", flag)
		}
	}
	if !strings.Contains(skill.Content, "rgt skill list") {
		t.Error("bootstrap skill does not tell the agent how to list skills")
	}
}
