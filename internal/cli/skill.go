package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/skills"
	"github.com/bonez-io/re_gent/internal/style"
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
	var server string
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List the skills this build can install",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			all := skills.All()
			source := "built into this rgt"
			if cwd, err := os.Getwd(); err == nil {
				if base := registryURL(remote.OSEnv, cwd, server); base != "" {
					if entries, err := fetchCatalog(cmd.Context(), base); err == nil {
						all = all[:0]
						for _, entry := range entries {
							all = append(all, skills.Skill{Name: entry.Name, Description: entry.Description, AllowedTools: entry.AllowedTools})
						}
						source = base
					}
				}
			}
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
			fmt.Fprintf(out, "\n%d skills from %s. Install with: rgt skill install <name>...\n", len(all), source)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&server, "server", "", "Registry to read from (defaults to this project's server)")
	return cmd
}

func skillInstallCmd() *cobra.Command {
	var (
		force  bool
		agent  string
		all    bool
		server string
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
			targets, err := skillTargetsFor(cwd, agent)
			if err != nil {
				return err
			}
			labels := make([]string, 0, len(targets))
			for _, target := range targets {
				labels = append(labels, target.label)
			}

			base := registryURL(remote.OSEnv, cwd, server)

			out := cmd.OutOrStdout()
			var installed, skipped, edited int
			var failures []string

			for _, name := range names {
				if reason := skills.Withheld(name); reason != "" {
					fmt.Fprintf(out, "  %s %s is not shipped by default: %s\n", style.Warning("!"), name, reason)
				}
				content, origin, resolveErr := resolveSkill(cmd.Context(), base, name)
				if resolveErr != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", name, resolveErr))
					fmt.Fprintf(out, "  %s %s: %v\n", style.Warning("!"), name, resolveErr)
					continue
				}
				for _, target := range targets {
					path, written, installErr := skills.InstallContent(target.dir, name, content, force)
					switch {
					case skills.IsExists(installErr):
						// Their edit outranks our copy. Say so and name the override.
						edited++
						fmt.Fprintf(out, "  %s %s left alone in %s (edited locally; --force to replace)\n", style.DimText("-"), name, target.label)
					case installErr != nil:
						failures = append(failures, fmt.Sprintf("%s in %s: %v", name, target.label, installErr))
						fmt.Fprintf(out, "  %s %s\n", style.Warning("!"), installErr)
					case !written:
						skipped++
						fmt.Fprintf(out, "  %s %s already current in %s\n", style.DimText("-"), name, target.label)
					default:
						installed++
						fmt.Fprintf(out, "  %s %s -> %s %s\n", style.Success(""), name, rel(cwd, path), style.DimText("("+string(origin)+")"))
						// The grant is printed for every install, whatever the source.
						// A skill fetched from a server is exactly the case where the
						// user most needs to see what it is permitted to run.
						if grant := frontMatterLine(content, "allowed-tools"); grant != "" {
							fmt.Fprintf(out, "      may run: %s\n", style.DimText(grant))
						}
					}
				}
			}

			fmt.Fprintf(out, "\n%d installed, %d already current, %d left alone (%s)\n", installed, skipped, edited, strings.Join(labels, ", "))
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
	cmd.Flags().StringVar(&agent, "agent", "auto", "Agent skill directory: auto, claude, codex, opencode, pi, or all")
	cmd.Flags().StringVar(&server, "server", "", "Registry to install from (defaults to this project's server)")
	return cmd
}

type skillInstallTarget struct {
	dir   string
	label string
}

// skillTargetsFor resolves every host that should receive an installed skill.
// Auto follows the bootstrap skill written by `rgt init`; a repo wired for two
// hosts therefore gets the skill in both places instead of silently defaulting
// to Claude and leaving Codex (or another host) unable to load it.
func skillTargetsFor(projectRoot, agent string) ([]skillInstallTarget, error) {
	target := func(dir, label string) skillInstallTarget {
		return skillInstallTarget{dir: filepath.Join(projectRoot, dir, "skills"), label: label + "/skills"}
	}
	all := []skillInstallTarget{
		target(".claude", ".claude"),
		target(".agents", ".agents"),
		target(".opencode", ".opencode"),
		target(".pi", ".pi"),
	}
	switch strings.ToLower(agent) {
	case "auto", "":
		var wired []skillInstallTarget
		for _, candidate := range all {
			if _, err := os.Stat(filepath.Join(candidate.dir, skills.Bootstrap, "SKILL.md")); err == nil {
				wired = append(wired, candidate)
			}
		}
		if len(wired) > 0 {
			return wired, nil
		}
		// Older projects predate the bootstrap helper. Fall back to the host
		// directories/config they already carry before choosing the historical
		// Claude default.
		markers := [][]string{{".claude"}, {".agents", ".codex"}, {".opencode"}, {".pi"}}
		for i, candidates := range markers {
			for _, marker := range candidates {
				if _, err := os.Stat(filepath.Join(projectRoot, marker)); err == nil {
					wired = append(wired, all[i])
					break
				}
			}
		}
		if len(wired) > 0 {
			return wired, nil
		}
		// Backwards-compatible fallback for projects created before the
		// bootstrap skill or host markers existed.
		return all[:1], nil
	case "claude", "claude-code", "claude_code":
		return all[0:1], nil
	case "codex", "agents":
		return all[1:2], nil
	case "opencode":
		return all[2:3], nil
	case "pi":
		return all[3:4], nil
	case "all", "both":
		return all, nil
	default:
		return nil, fmt.Errorf("unknown agent %q: use auto, claude, codex, opencode, pi, or all", agent)
	}
}

func rel(base, path string) string {
	if relative, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(relative, "..") {
		return relative
	}
	return path
}

// frontMatterLine reads one key from a SKILL.md's front matter. Fetched skills
// are plain text, so their grant is read from the bytes about to be written
// rather than from the embedded copy, which may differ.
func frontMatterLine(text, key string) string {
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
