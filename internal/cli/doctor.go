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

// findingSeverity separates the two kinds of bad news doctor can deliver.
//
// The distinction exists because doctor's exit code is also the installer's
// exit code: `curl … | sh` ends in `rgt doctor` and unwinds on a non-zero
// status. Without the distinction, everything doctor disliked was grounds to
// abort a working install — which is what happened to unconfigured git
// identity, the one condition a fresh VPS, container image or CI runner is
// almost guaranteed to be in.
type findingSeverity int

const (
	// severityFailure means capture cannot work until this is fixed: no
	// repository, no hooks, no agent host. The zero value, so a finding that
	// says nothing about severity blocks — new checks have to opt in to being
	// merely advisory, never out of it by omission.
	severityFailure findingSeverity = iota
	// severityWarning means capture works but something about it is degraded.
	// Worth interrupting a person for, not worth undoing an install for.
	severityWarning
)

// doctorFinding is one checked fact about this machine.
type doctorFinding struct {
	Name string
	OK   bool
	// Severity is only consulted when OK is false.
	Severity findingSeverity
	Detail   string
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

			// Only failure-level findings set the exit code. Doctor's question
			// is "will this project capture anything", and a warning is a
			// finding where the answer is still yes. Warnings are printed
			// either way — they are reported, just not fatal.
			if hasFailures(findings) {
				return fmt.Errorf("re_gent is not fully wired up in %s", cwd)
			}
			return nil
		},
	}
}

// diagnose collects every check without stopping at the first failure, so one
// run tells the user everything that is wrong rather than one thing at a time.
func diagnose(projectRoot string) []doctorFinding {
	findings := []doctorFinding{repositoryFinding(projectRoot), identityFinding()}

	// Only report on agents that are actually present here. Reporting a
	// missing Codex hook on a machine with no Codex is noise, and noise is
	// what makes people stop reading the output.
	agentsChecked := false
	if agentPresent(projectRoot, ".claude", "claude") {
		findings = append(findings, claudeHookFinding(projectRoot))
		agentsChecked = true
	}
	if agentPresent(projectRoot, ".codex", "codex") {
		findings = append(findings, codexHookFinding(projectRoot))
		agentsChecked = true
	}

	// Whether a hook is configured and whether it can run are two facts, so they
	// are two findings. Merging them into the hook checks would have made every
	// test that asserts "a hook is wired here" also depend on what this machine
	// happens to have on PATH.
	if agentsChecked {
		findings = append(findings, hookBinaryFinding(projectRoot))
	}

	// Sync-on-push is checked whenever there is something it could deliver —
	// i.e. whenever an agent is wired. Advisory only; see gitHookFinding.
	if agentsChecked {
		if f, present := gitHookFinding(projectRoot); present {
			findings = append(findings, f)
		}
	}

	if !agentsChecked {
		findings = append(findings, doctorFinding{
			Name:   "agents",
			OK:     false,
			Detail: "no agent host detected here; run rgt init --agent claude to wire one explicitly",
		})
	}

	return findings
}

// identityFinding reports whether this machine can name who is doing the work.
//
// Author comes from git identity and nowhere else — deliberately, since a
// second identity system would be a login to build, run, and keep in sync. The
// weakness of that choice is silence: with git identity unset, every step is
// still recorded, just anonymously, and nothing says so until someone looks at
// a session listing months later and finds no one to ask. Saying it here is the
// signal at the only moment it is still cheap to act on.
//
// It is a warning and not a failure. Capture works without it — the steps are
// recorded, only unattributed — and doctor is the last line of the `curl … |
// sh` installer, so failing here aborted installs on precisely the machines
// that have no identity to find: fresh VPS, container image, CI runner. The
// install had already done everything it needed to do by the time this ran.
func identityFinding() doctorFinding {
	author := capture.ResolveAuthor()
	if author.Name == "" && author.Email == "" {
		return doctorFinding{
			Name:     "git identity",
			OK:       false,
			Severity: severityWarning,
			Detail:   "no git identity configured; steps will be recorded anonymously, with no author. Run git config --global user.name \"Your Name\" and git config --global user.email you@example.com",
		}
	}
	return doctorFinding{Name: "git identity", OK: true, Detail: formatAuthor(author)}
}

// hookBinaryFinding reports whether the hooks configured here can actually be
// launched on this machine.
//
// This is the check that was missing when `rgt connect` began telling people to
// commit .claude/settings.json (#23). The installer writes the absolute path of
// its own binary into that file — correctly, because a hook runs in the agent
// host's environment and cannot rely on PATH — and a teammate who clones it
// inherits a path that exists on exactly one machine. Agent hosts do not report
// a hook that failed to launch, and doctor was satisfied by the file being
// present and mentioning re_gent, so nothing anywhere said that capture had
// stopped.
//
// A command that names no absolute path at all is left alone. That form defers
// to the agent host's PATH by design, and the PATH doctor can see is this
// terminal's, not the host's — failing on it would be doctor asserting something
// it is not in a position to know.
func hookBinaryFinding(projectRoot string) doctorFinding {
	var unreachable []string
	for _, command := range regentHookCommands(projectRoot) {
		paths, names := hookBinaryCandidates(command)
		if len(paths) == 0 {
			continue
		}
		if anyExecutableFile(paths) || anyOnPath(names) {
			continue
		}
		unreachable = appendUnique(unreachable, paths...)
	}

	if len(unreachable) == 0 {
		return doctorFinding{Name: "hook binary", OK: true, Detail: "the configured hooks resolve to a binary on this machine"}
	}
	return doctorFinding{
		Name: "hook binary",
		OK:   false,
		Detail: fmt.Sprintf(
			"the hooks configured here run %s, which is not on this machine — nothing is being captured\n"+
				"that path belongs to whoever ran the install; a clone carries it unchanged\n"+
				"install rgt and put it on PATH, or wire this machine: rgt init",
			strings.Join(unreachable, ", ")),
	}
}

// regentHookCommands returns every re_gent hook command configured for this
// project, across the agent configs that hold command strings.
func regentHookCommands(projectRoot string) []string {
	var commands []string

	settingsPath := filepath.Join(projectRoot, ".claude", "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var settings map[string]interface{}
		if json.Unmarshal(data, &settings) == nil {
			commands = append(commands, claudeHookCommandsIn(settings)...)
		}
	}

	// The Codex config is read line by line for the same reason codexHookFinding
	// reads it that way: the answer does not need the schema, only the commands.
	configPath := filepath.Join(projectRoot, ".codex", "config.toml")
	if data, err := os.ReadFile(configPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if isRegentHookCommand(line) {
				commands = append(commands, line)
			}
		}
	}

	return commands
}

// claudeHookCommandsIn walks the settings shape Claude Code uses and returns the
// commands that are ours. It is claudeSettingsHaveRegentHook's walk, collecting
// instead of stopping at the first match, so the two cannot disagree about what
// counts as one of our hooks.
func claudeHookCommandsIn(settings map[string]interface{}) []string {
	var commands []string
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return nil
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
				if command, _ := em["command"].(string); isRegentHookCommand(command) {
					commands = append(commands, command)
				}
			}
		}
	}
	return commands
}

// hookBinaryCandidates splits a hook command into the binaries it could end up
// running: absolute paths, and bare names left for PATH to resolve.
//
// Both kinds are collected because the shared hook command names both — an
// absolute path first and a PATH fallback behind it, see sharedHookCommand — and
// the command is only broken when neither can be reached. Fields are stripped of
// the quoting and shell punctuation the surrounding expression leaves attached,
// the same way isRegentHookCommand does it.
func hookBinaryCandidates(command string) (paths, names []string) {
	for _, field := range strings.Fields(command) {
		field = strings.Trim(field, `"'[],`)
		switch {
		case strings.HasPrefix(field, "/"):
			paths = appendUnique(paths, field)
		case field == "rgt" || field == "regent":
			names = appendUnique(names, field)
		}
	}
	return paths, names
}

func anyExecutableFile(paths []string) bool {
	for _, path := range paths {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

func anyOnPath(names []string) bool {
	for _, name := range names {
		if commandExists(name) {
			return true
		}
	}
	return false
}

func appendUnique(dst []string, values ...string) []string {
	for _, value := range values {
		found := false
		for _, existing := range dst {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, value)
		}
	}
	return dst
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

	// A hook in the right file is not the same fact as a hook the agent loads,
	// and neither is the same fact as a hook that records *here*. This check
	// reads the same path init wrote and used to stop here, which is how a
	// project whose agent is opened one directory up got a clean bill of health
	// while capture never started. See shadowingClaudeWorkspace.
	//
	// It is a warning whether or not the ancestor is wired, because in neither
	// case does doctor know the thing that would make it a verdict: which
	// directory the user opens their agent in. It can only see that some
	// ancestor has a .claude/ — true on any machine where an agent was ever
	// started above this project, and true of a directory holding nothing but
	// the settings.local.json Claude Code writes by itself.
	//
	// This first shipped as a failure when the ancestor was unwired, reasoning
	// that a session opened up there would capture nothing anywhere. The
	// reasoning holds; the certainty did not. The owner of this repo opens his
	// agent inside his projects, and the one-line installer ended "Setup ran,
	// but verification failed. Nothing will be captured until those problems
	// are fixed." over a project that captures perfectly — the same "reports
	// something other than what is true" defect this check was built to remove,
	// aimed the other way. Doctor's exit code is the installer's exit code (see
	// findingSeverity), so a false verdict here unwinds a good install.
	//
	// It never reports OK. The hazard is real and has to be said; it is simply
	// not something doctor is in a position to decide (#27, and #29 for letting
	// the user record the answer once).
	if shadow := shadowingClaudeWorkspace(projectRoot); shadow.found() {
		return doctorFinding{
			Name:     "claude hooks",
			OK:       false,
			Severity: severityWarning,
			Detail:   fmt.Sprintf("wrote %s\n%s", path, claudeShadowRemedy(projectRoot, shadow)),
		}
	}

	return doctorFinding{Name: "claude hooks", OK: true, Detail: path}
}

// claudeSettingsFileHasRegentHook answers claudeSettingsHaveRegentHook's
// question for a path. A missing or unparseable file counts as "no hook",
// because to the agent that would load it the effect is the same.
func claudeSettingsFileHasRegentHook(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}
	return claudeSettingsHaveRegentHook(settings)
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
				if command, _ := em["command"].(string); isRegentHookCommand(command) {
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
	//
	// There used to be a second, looser check here — any line containing both
	// "rgt " and "hook" — which existed only because the first one was too
	// strict to recognise our own hooks. Both were keyed on the binary's name.
	// One predicate that asks the right question replaces the pair.
	for _, line := range strings.Split(string(data), "\n") {
		if isRegentHookCommand(line) {
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

// allOK reports whether every check passed, warnings included. It answers "is
// there anything to tell the user", which is a different question from whether
// doctor should fail — see hasFailures.
func allOK(findings []doctorFinding) bool {
	for _, f := range findings {
		if !f.OK {
			return false
		}
	}
	return true
}

// hasFailures reports whether anything found would stop capture from working.
// This, and not allOK, is what decides doctor's exit code, and through it
// whether the one-line installer treats the install as ruined.
func hasFailures(findings []doctorFinding) bool {
	for _, f := range findings {
		if !f.OK && f.Severity == severityFailure {
			return true
		}
	}
	return false
}

// printFindings indents every line of a detail, not just the first.
//
// A finding whose explanation is two facts and a command to run reads as three
// lines; the single-line version put the third one hard against the left margin
// where it no longer looked like part of the finding above it.
func printFindings(findings []doctorFinding) {
	fmt.Println()
	for _, f := range findings {
		if f.OK {
			fmt.Printf("  %s %s\n", style.Success(""), f.Name)
			printDetail(f.Detail, style.DimText)
			continue
		}
		// Warnings and failures used to render identically, which left the
		// reader to work out from the prose which lines were the reason the
		// command exited non-zero. The marker now says it.
		marker := style.Error("")
		if f.Severity == severityWarning {
			marker = style.Warning("")
		}
		fmt.Printf("  %s %s\n", marker, f.Name)
		printDetail(f.Detail, func(line string) string { return line })
	}
	fmt.Println()
}

func printDetail(detail string, render func(string) string) {
	if detail == "" {
		return
	}
	for _, line := range strings.Split(detail, "\n") {
		fmt.Printf("      %s\n", render(line))
	}
}
