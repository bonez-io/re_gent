package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/regent-vcs/regent/internal/skills"
	"github.com/regent-vcs/regent/internal/style"
	"github.com/spf13/cobra"
)

// SkillCmd returns `rgt skill`, the installer for re_gent's agent skills.
//
// Skills are how re_gent's history becomes usable inside an agent: each one is
// a prompt plus a tool grant that turns "why is this line here" into a command
// the agent already knows how to run. They are only loaded from disk, so
// getting one into a project means writing a file — which the browser cannot
// do. This command is what the Skills view's copy button points at.
func SkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "skill",
		Short:        "Install and list re_gent agent skills",
		SilenceUsage: true,
		RunE:         func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(skillListCmd(), skillInstallCmd())
	return cmd
}

func skillListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List the skills this build can install",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			all := skills.All()
			out := cmd.OutOrStdout()
			if jsonOut {
				fmt.Fprintln(out, "[")
				for i, skill := range all {
					comma := ","
					if i == len(all)-1 {
						comma = ""
					}
					fmt.Fprintf(out, "  {%q: %q, %q: %q, %q: %q}%s\n",
						"name", skill.Name, "description", skill.Description, "allowed_tools", skill.AllowedTools, comma)
				}
				fmt.Fprintln(out, "]")
				return nil
			}
			for _, skill := range all {
				fmt.Fprintf(out, "  %s\n      %s\n", style.Brand(skill.Name), truncate(skill.Description, 96))
			}
			fmt.Fprintf(out, "\n%d skills. Install with: rgt skill install <name>...\n", len(all))
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func skillInstallCmd() *cobra.Command {
	var (
		force bool
		agent string
		all   bool
	)
	cmd := &cobra.Command{
		Use:   "install <name>...",
		Short: "Install one or more skills into this project",
		Long: "Write re_gent's agent skills into this project so an agent can load them.\n\n" +
			"Each skill declares the commands it is allowed to run; that grant is printed\n" +
			"as it is installed, because a skill is executable instruction, not a note.\n\n" +
			"A skill file you have edited is never overwritten without --force.",
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && !all {
				return fmt.Errorf("name at least one skill, or pass --all\n\n  rgt skill list             see what is available\n  rgt skill install blame    install one")
			}
			names := args
			if all {
				names = skills.DefaultNames()
			}

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			dir, label, err := skillsDirFor(cwd, agent)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			var installed, skipped, edited int
			var failures []string

			for _, name := range names {
				if reason := skills.Withheld(name); reason != "" {
					fmt.Fprintf(out, "  %s %s is not shipped by default: %s\n", style.Warning("!"), name, reason)
				}
				path, written, err := skills.Install(dir, name, force)
				switch {
				case skills.IsExists(err):
					// Their edit outranks our copy. Say so and name the override.
					edited++
					fmt.Fprintf(out, "  %s %s left alone (edited locally; --force to replace)\n", style.DimText("-"), name)
				case err != nil:
					failures = append(failures, fmt.Sprintf("%s: %v", name, err))
					fmt.Fprintf(out, "  %s %s\n", style.Warning("!"), err)
				case !written:
					skipped++
					fmt.Fprintf(out, "  %s %s already current\n", style.DimText("-"), name)
				default:
					installed++
					skill, _ := skills.Get(name)
					fmt.Fprintf(out, "  %s %s -> %s\n", style.Success(""), name, rel(cwd, path))
					if skill.AllowedTools != "" {
						fmt.Fprintf(out, "      may run: %s\n", style.DimText(skill.AllowedTools))
					}
				}
			}

			fmt.Fprintf(out, "\n%d installed, %d already current, %d left alone (%s)\n", installed, skipped, edited, label)
			if installed > 0 {
				fmt.Fprintf(out, "%s Restart the agent session in this repo — skills load at startup.\n", style.Warning(""))
			}
			if len(failures) > 0 {
				return fmt.Errorf("%d skill(s) failed: %s", len(failures), strings.Join(failures, "; "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Replace a skill file you have edited")
	cmd.Flags().BoolVar(&all, "all", false, "Install every skill this build ships")
	cmd.Flags().StringVar(&agent, "agent", "claude", "Which agent's skill directory to write: claude or codex")
	return cmd
}

// skillsDirFor resolves where a host loads skills from.
//
// Skill directories are host conventions, not re_gent's: Claude Code reads
// .claude/skills, and the Codex-style hosts read .agents/skills. Writing to the
// wrong one installs a file nothing will ever load, so the choice is explicit
// rather than guessed.
func skillsDirFor(projectRoot, agent string) (dir string, label string, err error) {
	switch strings.ToLower(agent) {
	case "claude", "claude-code", "claude_code", "":
		return filepath.Join(projectRoot, ".claude", "skills"), ".claude/skills", nil
	case "codex", "agents":
		return filepath.Join(projectRoot, ".agents", "skills"), ".agents/skills", nil
	default:
		return "", "", fmt.Errorf("unknown agent %q: use claude or codex", agent)
	}
}

func rel(base, path string) string {
	if relative, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(relative, "..") {
		return relative
	}
	return path
}
