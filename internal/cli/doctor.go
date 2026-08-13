package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/regent-vcs/regent/internal/capture"
	"github.com/regent-vcs/regent/internal/style"
	"github.com/spf13/cobra"
)

// doctorFinding is one checked fact about this machine.
type doctorFinding struct {
	Name   string
	OK     bool
	Detail string
}

// DoctorCmd reports whether re_gent is actually wired up here.
//
// It is the answer to the failure this project is most exposed to: setup
// completes, the hook file is written or silently is not, and every other
// command exits 0 regardless. Nothing tells the user that nothing is being
// captured until they go looking for history that was never recorded.
//
// Doctor is deliberately local-only. It reads configuration on this machine and
// prints to this terminal. It contacts nothing and reports nothing anywhere.
func DoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "doctor",
		Short:        "Check that re_gent is wired up and capturing",
		Long:         "Inspects this project's re_gent repository and agent hook configuration, and reports anything that would stop capture from working. Runs entirely locally.",
		SilenceUsage: true,
		Annotations: map[string]string{
			"commandOrder": "1",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}

			findings := diagnose(cwd)
			printFindings(findings)

			if !allOK(findings) {
				return fmt.Errorf("re_gent is not fully wired up in %s", cwd)
			}
			return nil
		},
	}
}

// diagnose collects every check without stopping at the first failure, so one
// run tells the user everything that is wrong rather than one thing at a time.
func diagnose(projectRoot string) []doctorFinding {
	findings := []doctorFinding{repositoryFinding(projectRoot)}

	// Only report on agents that are actually present here. Reporting a
	// missing Codex hook on a machine with no Codex is noise, and noise is
	// what makes people stop reading the output.
	if agentPresent(projectRoot, ".claude", "claude") {
		findings = append(findings, claudeHookFinding(projectRoot))
	}
	if agentPresent(projectRoot, ".codex", "codex") {
		findings = append(findings, codexHookFinding(projectRoot))
	}

	if len(findings) == 1 {
		findings = append(findings, doctorFinding{
			Name:   "agents",
			OK:     false,
			Detail: "no agent host detected here; run rgt init --agent claude to wire one explicitly",
		})
	}

	return findings
}

func repositoryFinding(projectRoot string) doctorFinding {
	regentDir := filepath.Join(projectRoot, ".regent")
	if !pathExists(regentDir) {
		return doctorFinding{
			Name:   "repository",
			OK:     false,
			Detail: "no .regent/ directory here; run rgt init",
		}
	}
	return doctorFinding{Name: "repository", OK: true, Detail: regentDir}
}

func claudeHookFinding(projectRoot string) doctorFinding {
	path := filepath.Join(projectRoot, ".claude", "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return doctorFinding{
			Name:   "claude hooks",
			OK:     false,
			Detail: fmt.Sprintf("cannot read %s: %v", path, err),
		}
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return doctorFinding{
			Name:   "claude hooks",
			OK:     false,
			Detail: fmt.Sprintf("%s is not valid JSON: %v", path, err),
		}
	}

	if !claudeSettingsHaveRegentHook(settings) {
		return doctorFinding{
			Name:   "claude hooks",
			OK:     false,
			Detail: fmt.Sprintf("no re_gent hook in %s; nothing will be captured. Run rgt init --agent claude", path),
		}
	}

	return doctorFinding{Name: "claude hooks", OK: true, Detail: path}
}

// claudeSettingsHaveRegentHook walks the settings shape Claude Code uses:
// hooks -> event name -> groups -> hooks -> {type, command}. It reuses the same
// normalizers the installer uses so the two cannot drift apart.
func claudeSettingsHaveRegentHook(settings map[string]interface{}) bool {
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false
	}
	for event := range hooks {
		for _, group := range normalizeHookGroups(hooks[event]) {
			gm, ok := group.(map[string]interface{})
			if !ok {
				continue
			}
			entries, _ := normalizeHookArray(gm["hooks"])
			for _, entry := range entries {
				em, ok := entry.(map[string]interface{})
				if !ok {
					continue
				}
				if command, _ := em["command"].(string); capture.IsRegentCommand(command) {
					return true
				}
			}
		}
	}
	return false
}

func codexHookFinding(projectRoot string) doctorFinding {
	path := filepath.Join(projectRoot, ".codex", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return doctorFinding{
			Name:   "codex hooks",
			OK:     false,
			Detail: fmt.Sprintf("cannot read %s: %v", path, err),
		}
	}

	// The Codex config is TOML with command strings scattered across hook
	// tables. Checking each line against the same predicate the installer
	// writes is enough to answer "is a re_gent hook present", without this
	// file needing to know the schema.
	for _, line := range strings.Split(string(data), "\n") {
		if capture.IsRegentCommand(strings.Trim(strings.TrimSpace(line), `"'`)) {
			return doctorFinding{Name: "codex hooks", OK: true, Detail: path}
		}
		if strings.Contains(line, "rgt ") && strings.Contains(line, "hook") {
			return doctorFinding{Name: "codex hooks", OK: true, Detail: path}
		}
	}

	return doctorFinding{
		Name:   "codex hooks",
		OK:     false,
		Detail: fmt.Sprintf("no re_gent hook in %s; nothing will be captured. Run rgt init --agent codex", path),
	}
}

// agentPresent mirrors the detection resolveAgentTargets uses, so doctor checks
// exactly the agents init would have wired.
func agentPresent(projectRoot, markerDir, binary string) bool {
	return pathExists(filepath.Join(projectRoot, markerDir)) || commandExists(binary)
}

func allOK(findings []doctorFinding) bool {
	for _, f := range findings {
		if !f.OK {
			return false
		}
	}
	return true
}

func printFindings(findings []doctorFinding) {
	fmt.Println()
	for _, f := range findings {
		if f.OK {
			fmt.Printf("  %s %s\n", style.Success(""), f.Name)
			if f.Detail != "" {
				fmt.Printf("      %s\n", style.DimText(f.Detail))
			}
			continue
		}
		fmt.Printf("  %s %s\n", style.Warning(""), f.Name)
		if f.Detail != "" {
			fmt.Printf("      %s\n", f.Detail)
		}
	}
	fmt.Println()
}
