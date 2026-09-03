package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/skills"
	"github.com/bonez-io/re_gent/internal/store"
	"github.com/bonez-io/re_gent/internal/style"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

type agentTarget string

type hookInstallResult struct {
	BackupPath string
}

const (
	agentAuto     agentTarget = "auto"
	agentClaude   agentTarget = "claude"
	agentCodex    agentTarget = "codex"
	agentOpenCode agentTarget = "opencode"
	agentPi       agentTarget = "pi"
	agentBoth     agentTarget = "both"
	agentAll      agentTarget = "all"

	claudeUserHookArgs      = "message-hook user"
	claudeAssistantHookArgs = "message-hook assistant"
	claudeToolBatchHookArgs = "tool-batch-hook"
	codexHookArgs           = "codex-hook"
	piPackageSource         = "git:github.com/MegaGrindStone/regent-pi-extension"
	piInstallCommand        = "pi install -l " + piPackageSource
)

func InitCmd() *cobra.Command {
	var skipHook bool
	var noGitHook bool
	var skipSkills bool
	var withSkills bool
	var agent string
	var captureRoot string

	cmd := &cobra.Command{
		Use:   "init [server-url]",
		Short: "Initialize a new re_gent repository, or connect one to a server",
		Long: `Creates a .regent directory in the current workspace and sets up agent hooks.

Pass a server URL and init behaves exactly like "rgt connect <url>",
--setup included: this is the one-command form the README and the self-hosted
onboarding wizard print ("curl ... | sh && rgt init <url> --setup <code>"),
so a fresh machine never needs to know that init and connect are two
commands. With no URL, init keeps its local-only behavior below.`,
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		Annotations: map[string]string{
			"commandOrder": "0",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// The alias: "rgt init <url>" is "rgt connect <url>". Dispatched
			// first and unconditionally on any positional argument, before any
			// of init's own local-repository logic runs, because the two
			// behaviors are mutually exclusive — a URL means "connect this to a
			// server," never "also initialize it locally first." connect does
			// its own .regent/ initialization when needed.
			//
			// This reuses runConnectRunE (internal/cli/connect.go) rather than
			// reimplementing it: cmd here is init's own *cobra.Command, but
			// every flag runConnectRunE reads (as, no-git-hook, agent, url,
			// yes, org, as-fork, setup) is also registered below on InitCmd's
			// FlagSet under the same name, so the lookups resolve exactly as
			// they would from ConnectCmd.
			if len(args) > 0 {
				return runConnectRunE(cmd, args)
			}
			if captureRoot != "" && captureRoot != "project" && captureRoot != "workspace" {
				return fmt.Errorf("invalid --capture-root %q: use project or workspace", captureRoot)
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			out := cmd.OutOrStdout()
			flow := style.NewFlow(out)
			flow.Header("init", filepath.Base(cwd))

			targets, err := resolveAgentTargets(cwd, agentTarget(agent))
			if err != nil {
				return err
			}
			input := bufio.NewReader(os.Stdin)

			reinit := pathExists(filepath.Join(cwd, ".regent"))
			// Resolved before anything is printed, because it decides what the
			// whole of step 1 is even about. See serverBinding.
			binding := resolveServerBinding(cwd)

			var s *store.Store
			if reinit {
				s, err = store.Open(filepath.Join(cwd, ".regent"))
				if err != nil {
					return err
				}
				idx, err := index.Open(s)
				if err != nil {
					return fmt.Errorf("open index: %w", err)
				}
				defer func() { _ = idx.Close() }()

				if !binding.bound() {
					Verbosef(out, "  using existing .regent/\n")
				}
			} else {
				s, err = store.Init(cwd)
				if err != nil {
					return err
				}

				idx, err := index.Open(s)
				if err != nil {
					return fmt.Errorf("initialize index: %w", err)
				}
				defer func() { _ = idx.Close() }()

				if err := createRegentGitignore(cwd); err != nil {
					Verbosef(out, "  could not create .regent/.gitignore: %v\n", err)
				}

				if !binding.bound() {
					Verbosef(out, "  initialized .regent/\n")
				}
			}
			if captureRoot != "" {
				if err := recordCaptureRoot(s, captureRoot); err != nil {
					return err
				}
				Verbosef(out, "  capture root set to %s\n", captureRoot)
			}
			if binding.bound() {
				Verbosef(out, "  connected to %s (repo: %s)\n", binding.url, binding.repoID)
			}
			flow.Step("Repository ready")

			if reinit && Verbose() {
				printExistingHooks(cwd)
			}
			var outcome hookOutcome
			var hookOutput bytes.Buffer
			hookErr := flow.Wait("Configuring agent integrations", func() error {
				var configureErr error
				outcome, configureErr = configureHooksTo(cwd, targets, hookOptions{skip: skipHook, noGitHook: noGitHook}, &hookOutput)
				return configureErr
			})
			if hookOutput.Len() > 0 {
				_, _ = io.Copy(out, &hookOutput)
			}
			if hookErr != nil {
				Verbosef(out, "  %v\n", hookErr)
				if Verbose() {
					printManualInstructions(targets)
				}
			}

			// The bootstrap skill is installed unconditionally, while the rest
			// stay opt-in. It is the only one that makes the others findable: a
			// project without it has a catalog nothing inside the agent knows to
			// look at, so gating discovery behind a flag hides the feature from
			// everyone who does not already know it exists. It grants two
			// read-mostly rgt commands and nothing else.
			if !skipSkills {
				if err := installBootstrapSkill(cwd, outcome.installed); err != nil {
					fmt.Printf("  %s Could not install the skills helper: %v\n", style.Warning(""), err)
				}
			}

			if withSkills && !skipSkills {
				if err := offerSkillInstall(cwd, outcome.installed, input); err != nil {
					flow.Warning("Optional agent skills were not installed")
					Verbosef(out, "  %v\n", err)
				}
			}

			// The summary reports what was installed, not what was detected,
			// and the exit code follows it. A run that wired nothing must not
			// look like a success to a script, a devcontainer, or a teammate.
			printSummary(out, cwd, outcome, binding)
			if hookErr != nil {
				return fmt.Errorf("configure hooks: %w", hookErr)
			}
			if !skipHook && len(outcome.installed) == 0 {
				return errors.New("no agent hooks were configured; pass --agent to name one explicitly, or --skip-hook to proceed without capture")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&skipHook, "skip-hook", false, "Skip automatic hook configuration")
	cmd.Flags().BoolVar(&noGitHook, "no-git-hook", false, "Do not install the Git pre-push hook that syncs queued history on git push")
	cmd.Flags().BoolVar(&skipSkills, "skip-skills", false, "Skip agent skill installation")
	_ = cmd.Flags().MarkHidden("skip-skills") // compatibility: skills are opt-in now
	cmd.Flags().BoolVar(&withSkills, "skills", false, "Offer to install optional agent skills")
	cmd.Flags().StringVar(&agent, "agent", string(agentAuto), "Agent hooks to configure: auto, claude, codex, opencode, pi, both, all")
	cmd.Flags().StringVar(&captureRoot, "capture-root", "", "Record this intentional capture layout: project or workspace")

	// Connect-only flags, present here solely so "rgt init <url> [flags]"
	// parses the same flags "rgt connect <url> [flags]" does — see the alias
	// dispatch above. They are read only by runConnectRunE, and only when a
	// URL argument sends this command down that path; the local-init RunE
	// below never consults them. Keep the names, defaults, and help text in
	// sync with ConnectCmd (internal/cli/connect.go).
	cmd.Flags().String("as", "", "identity (legacy server) or display name (project-id server) for this project, instead of deriving one")
	cmd.Flags().String("url", "", "public http(s) URL to prove and bind when provisioning an SSH target")
	cmd.Flags().Bool("yes", false, "provision an SSH target without asking for confirmation")
	cmd.Flags().String("org", "", "organization to enroll this project in (project-id servers only)")
	cmd.Flags().Bool("as-fork", false, "enroll a detected fork as its own project instead of stopping to ask")
	cmd.Flags().String("setup", "", "one-time setup code from the self-hosted onboarding wizard; exchanged for a machine credential before connecting")

	return cmd
}

// recordCaptureRoot persists the owner's answer without disturbing the remote
// binding connect may already have written to the same config file.
func recordCaptureRoot(s *store.Store, root string) error {
	cfg, err := s.ReadRepoConfig()
	if err != nil {
		return fmt.Errorf("read repo config: %w", err)
	}
	cfg.Capture.Root = root
	if err := s.WriteRepoConfig(cfg); err != nil {
		return fmt.Errorf("write capture layout: %w", err)
	}
	return nil
}

// serverBinding names the server a project's history belongs to.
//
// The zero value means "not bound", which is local mode and the default. Both
// fields are required for the same reason remote.Config.Enabled() requires
// both: half a binding is a typo, and a typo must read as local mode rather
// than as a server nobody can name.
type serverBinding struct {
	url    string
	repoID string
}

func (b serverBinding) bound() bool { return b.url != "" && b.repoID != "" }

// resolveServerBinding answers "where does this project's history live?" using
// the same resolution capture and the read commands use, so init cannot
// describe a project differently from the way it will actually behave.
//
// A configuration that fails to load is reported as unbound. Being wrong in
// that direction costs a line of output; being wrong the other way would have
// init announce a server for a project that has none.
func resolveServerBinding(cwd string) serverBinding {
	cfg, err := remote.LoadConfigForCWD(remote.OSEnv, cwd)
	if err != nil || !cfg.Enabled() {
		return serverBinding{}
	}
	return serverBinding{url: cfg.ServerURL, repoID: cfg.RepoID}
}

// printSummary takes a hookOutcome rather than []agentTarget so that the
// requested agents cannot be passed here by mistake. See hookOutcome.
func printSummary(out io.Writer, projectRoot string, outcome hookOutcome, binding serverBinding) {
	flow := style.NewFlow(out)
	headline, ok := summaryStatus(outcome)

	if ok {
		flow.Step(agentSummary(outcome.installed) + " hooks configured")
	} else {
		flow.Warning(headline)
	}
	if !ok {
		flow.Next("Run rgt init --agent <claude|codex|opencode|pi>")
		flow.End()
		return
	}
	// Name the place the user's history will actually be. Printing the local
	// .regent/ path for a server-bound project sends them to a directory that
	// holds a cache and a config file and none of their work.
	if binding.bound() {
		flow.Detail("Server", binding.url)
		flow.Detail("Project", binding.repoID)
	} else {
		flow.Detail("Store", filepath.Join(projectRoot, ".regent"))
	}
	flow.Complete("Ready to capture")
	flow.Next("Restart your agent here, then run rgt doctor")
	flow.End()
}

func agentSummary(installed []agentTarget) string {
	if len(installed) == 0 {
		return "Agent"
	}
	names := make([]string, 0, len(installed))
	for _, target := range installed {
		switch target {
		case agentClaude:
			names = append(names, "Claude")
		case agentCodex:
			names = append(names, "Codex")
		case agentOpenCode:
			names = append(names, "OpenCode")
		case agentPi:
			names = append(names, "Pi")
		}
	}
	return strings.Join(names, " + ")
}

func printHookInstallWarningTo(out io.Writer, result hookInstallResult) {
	if result.BackupPath == "" {
		return
	}
	fmt.Fprintf(out, "  %s Existing hook config was invalid; backed up to %s before rewriting\n", style.Warning(""), result.BackupPath)
}

func installClaudeHook(projectRoot string) (hookInstallResult, error) {
	return installClaudeHookWith(projectRoot, hookBinary())
}

// installClaudeHookWith writes the Claude hook for a named binary.
//
// The binary is a parameter rather than resolved inside, because the situation
// this file is most exposed to cannot be reproduced otherwise: a settings.json
// written on someone else's machine, naming a path that exists only there. Under
// `go test` the running executable is an ephemeral build, so resolveHookBinary
// deliberately returns the bare name and no test could produce the file a
// teammate actually clones. See TestACommittedHookRunsOnATeammatesMachineToo.
func installClaudeHookWith(projectRoot, binary string) (hookInstallResult, error) {
	var result hookInstallResult
	claudeDir := filepath.Join(projectRoot, ".claude")
	settingsPath := filepath.Join(claudeDir, "settings.json")

	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return result, fmt.Errorf("create .claude directory: %w", err)
	}

	settings := map[string]interface{}{}
	if data, err := os.ReadFile(settingsPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &settings); err != nil {
			backupPath, err := backupFile(settingsPath)
			if err != nil {
				return result, fmt.Errorf("backup invalid Claude settings: %w", err)
			}
			result.BackupPath = backupPath
			settings = map[string]interface{}{}
		}
	}

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
		settings["hooks"] = hooks
	}

	mergeHookCommand(hooks, "UserPromptSubmit", sharedHookCommand(binary, claudeUserHookArgs))
	mergeHookCommand(hooks, "Stop", sharedHookCommand(binary, claudeAssistantHookArgs))
	mergeHookCommand(hooks, "PostToolBatch", sharedHookCommand(binary, claudeToolBatchHookArgs))
	removeRegentHookCommands(hooks, "PostToolUse")

	output, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return result, fmt.Errorf("marshal Claude settings: %w", err)
	}
	if err := os.WriteFile(settingsPath, output, 0o644); err != nil {
		return result, fmt.Errorf("write Claude settings: %w", err)
	}

	return result, nil
}

func installCodexHook(projectRoot string) (hookInstallResult, error) {
	return installCodexHookWith(projectRoot, hookBinary())
}

// installCodexHookWith writes portable Codex hooks for a named binary.
//
// Codex executes Unix command hooks through $SHELL -lc (or /bin/sh -lc), so
// the same absolute-path-then-PATH fallback used by Claude is safe here. Its
// commandWindows setting is executed by cmd.exe instead, so it needs the
// equivalent cmd expression rather than the POSIX-only `exec` form. Keeping
// the binary injectable lets the cross-machine behaviour be tested without
// relying on the ephemeral binary that `go test` runs.
func installCodexHookWith(projectRoot, binary string) (hookInstallResult, error) {
	var result hookInstallResult
	codexDir := filepath.Join(projectRoot, ".codex")
	configPath := filepath.Join(codexDir, "config.toml")

	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		return result, fmt.Errorf("create .codex directory: %w", err)
	}

	config := map[string]interface{}{}
	if data, err := os.ReadFile(configPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := toml.Unmarshal(data, &config); err != nil {
			backupPath, err := backupFile(configPath)
			if err != nil {
				return result, fmt.Errorf("backup invalid Codex config: %w", err)
			}
			result.BackupPath = backupPath
			config = map[string]interface{}{}
		}
	}

	hooks, _ := config["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
		config["hooks"] = hooks
	}

	for _, eventName := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"} {
		mergeCodexHookCommand(hooks, eventName, binary)
	}
	enableCodexHooksFeature(config)

	output, err := toml.Marshal(config)
	if err != nil {
		return result, fmt.Errorf("marshal Codex config: %w", err)
	}
	if err := os.WriteFile(configPath, output, 0o644); err != nil {
		return result, fmt.Errorf("write Codex config: %w", err)
	}

	return result, nil
}

func installOpenCodeHook(projectRoot string) error {
	opencodeDir := filepath.Join(projectRoot, ".opencode")
	if err := os.MkdirAll(opencodeDir, 0o755); err != nil {
		return fmt.Errorf("create .opencode directory: %w", err)
	}

	if err := npmInstallOpenCodePlugin(opencodeDir); err != nil {
		return err
	}

	return registerOpenCodePlugin(projectRoot)
}

// ensureOpenCodePackage gives .opencode its own package.json. Without one,
// npm walks up to the nearest package.json and installs into the user's
// project instead, which fails outright in pnpm workspaces ("catalog:") and
// would otherwise rewrite their dependencies.
func ensureOpenCodePackage(opencodeDir string) error {
	path := filepath.Join(opencodeDir, "package.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	manifest := "{\n  \"name\": \"regent-opencode-integration\",\n  \"private\": true,\n  \"description\": \"re_gent OpenCode plugin, managed by rgt connect\"\n}\n"
	return os.WriteFile(path, []byte(manifest), 0o644)
}

func npmInstallOpenCodePlugin(opencodeDir string) error {
	if err := ensureOpenCodePackage(opencodeDir); err != nil {
		return fmt.Errorf("prepare .opencode: %w", err)
	}
	cmd := exec.Command("npm", "install", "--save", "--prefix", opencodeDir, "@regent-vcs/opencode-plugin")
	cmd.Dir = opencodeDir
	return runSetupCommand(cmd, "install OpenCode integration")
}

func registerOpenCodePlugin(projectRoot string) error {
	configPath := findOpenCodeConfig(projectRoot)
	if configPath == "" {
		configPath = filepath.Join(projectRoot, "opencode.jsonc")
	}

	config := map[string]interface{}{}
	if data, err := os.ReadFile(configPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		cleaned := stripJSONComments(string(data))
		if err := json.Unmarshal([]byte(cleaned), &config); err != nil {
			config = map[string]interface{}{}
		}
	}

	pluginRef := "@regent-vcs/opencode-plugin"
	plugins, _ := config["plugin"].([]interface{})
	for _, p := range plugins {
		if s, ok := p.(string); ok && s == pluginRef {
			return nil
		}
	}
	config["plugin"] = append(plugins, pluginRef)

	output, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal OpenCode config: %w", err)
	}
	return os.WriteFile(configPath, output, 0o644)
}

func installPiHook(projectRoot string) bool {
	if piPackageConfigured(projectRoot) {
		Verbosef(os.Stdout, "  Pi extension package already configured\n")
		return false
	}
	if !commandExists("pi") {
		printPiInstallWarning("Pi executable not found on PATH")
		return false
	}

	cmd := exec.Command("pi", "install", "-l", piPackageSource)
	cmd.Dir = projectRoot
	if err := runSetupCommand(cmd, "install Pi integration"); err != nil {
		printPiInstallWarning(err.Error())
		return false
	}
	return true
}

func printPiInstallWarning(reason string) {
	Verbosef(os.Stdout, "  %s %s\n", style.Warning(""), reason)
	Verbosef(os.Stdout, "  %s Install the Pi extension manually with: %s\n", style.DimText("-"), piInstallCommand)
}

func piPackageConfigured(projectRoot string) bool {
	settingsPath := filepath.Join(projectRoot, ".pi", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return false
	}

	var settings map[string]any
	cleaned := stripJSONComments(string(data))
	if err := json.Unmarshal([]byte(cleaned), &settings); err != nil {
		return false
	}

	const packageName = "regent-pi-extension"
	switch packages := settings["packages"].(type) {
	case string:
		return strings.Contains(packages, packageName)
	case []any:
		for _, entry := range packages {
			switch typed := entry.(type) {
			case string:
				if strings.Contains(typed, packageName) {
					return true
				}
			case map[string]any:
				source, _ := typed["source"].(string)
				if strings.Contains(source, packageName) {
					return true
				}
			}
		}
	}
	return false
}

func findOpenCodeConfig(projectRoot string) string {
	candidates := []string{
		filepath.Join(projectRoot, "opencode.jsonc"),
		filepath.Join(projectRoot, "opencode.json"),
		filepath.Join(projectRoot, ".opencode.jsonc"),
		filepath.Join(projectRoot, ".opencode.json"),
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func stripJSONComments(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '/' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
		} else if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < len(s) {
				i += 2
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}

func enableCodexHooksFeature(config map[string]interface{}) {
	features, _ := config["features"].(map[string]interface{})
	if features == nil {
		features = map[string]interface{}{}
		config["features"] = features
	}
	features["hooks"] = true
}

func mergeHookCommand(hooks map[string]interface{}, eventName, command string) {
	groups := filterRegentHookCommands(normalizeHookGroups(hooks[eventName]))
	hooks[eventName] = append(groups, hookGroup(command))
}

// mergeCodexHookCommand adds the platform-specific forms Codex selects at
// runtime. Codex's `command` string is evaluated by a POSIX shell on Unix;
// `commandWindows` is evaluated by cmd.exe, where `exec` is not available.
func mergeCodexHookCommand(hooks map[string]interface{}, eventName, binary string) {
	groups := filterRegentHookCommands(normalizeHookGroups(hooks[eventName]))
	hooks[eventName] = append(groups, map[string]interface{}{
		"matcher": "",
		"hooks": []interface{}{map[string]interface{}{
			"type":           "command",
			"command":        sharedHookCommand(binary, codexHookArgs),
			"commandWindows": sharedHookCommandWindows(binary, codexHookArgs),
		}},
	})
}

func removeRegentHookCommands(hooks map[string]interface{}, eventName string) {
	groups := filterRegentHookCommands(normalizeHookGroups(hooks[eventName]))
	if len(groups) == 0 {
		delete(hooks, eventName)
		return
	}
	hooks[eventName] = groups
}

// normalizeHookArray normalizes arbitrary hook configuration values (from TOML
// or JSON) into a typed []interface{} slice. It returns (nil, false) when the
// value is nil so callers can distinguish "absent" from "empty". The string case
// in normalizeHookGroups means this function intentionally excludes it.
func normalizeHookArray(value interface{}) ([]interface{}, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false
	case []interface{}:
		return typed, true
	case []map[string]interface{}:
		items := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return items, true
	case map[string]interface{}:
		return []interface{}{typed}, true
	default:
		return []interface{}{typed}, true
	}
}

func normalizeHookGroups(value interface{}) []interface{} {
	if s, ok := value.(string); ok {
		return []interface{}{hookGroup(s)}
	}
	groups, _ := normalizeHookArray(value)
	return groups
}

func filterRegentHookCommands(groups []interface{}) []interface{} {
	filtered := make([]interface{}, 0, len(groups))
	for _, group := range groups {
		groupMap, ok := group.(map[string]interface{})
		if !ok {
			filtered = append(filtered, group)
			continue
		}

		hookEntries, hasHooks := normalizeHookArray(groupMap["hooks"])
		if !hasHooks {
			filtered = append(filtered, group)
			continue
		}

		nextHookEntries := make([]interface{}, 0, len(hookEntries))
		for _, hookEntry := range hookEntries {
			hookMap, ok := hookEntry.(map[string]interface{})
			if !ok {
				nextHookEntries = append(nextHookEntries, hookEntry)
				continue
			}
			command, _ := hookMap["command"].(string)
			if isRegentHookCommand(command) {
				continue
			}
			nextHookEntries = append(nextHookEntries, hookEntry)
		}
		if len(nextHookEntries) == 0 {
			continue
		}

		nextGroup := map[string]interface{}{}
		for key, value := range groupMap {
			nextGroup[key] = value
		}
		nextGroup["hooks"] = nextHookEntries
		filtered = append(filtered, nextGroup)
	}
	return filtered
}

func hookGroup(command string) map[string]interface{} {
	return map[string]interface{}{
		"matcher": "",
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": command,
			},
		},
	}
}

func printExistingHooks(projectRoot string) {
	fmt.Println("Currently configured:")
	fmt.Println()

	settingsPath := filepath.Join(projectRoot, ".claude", "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var settings map[string]interface{}
		if json.Unmarshal(data, &settings) == nil {
			if hooks, ok := settings["hooks"].(map[string]interface{}); ok && len(hooks) > 0 {
				for event := range hooks {
					groups := normalizeHookGroups(hooks[event])
					for _, g := range groups {
						if gm, ok := g.(map[string]interface{}); ok {
							entries, _ := normalizeHookArray(gm["hooks"])
							for _, e := range entries {
								if em, ok := e.(map[string]interface{}); ok {
									if cmd, _ := em["command"].(string); isRegentHookCommand(cmd) {
										fmt.Printf("  %s Claude Code\n", style.Success(""))
										goto doneClaudeCheck
									}
								}
							}
						}
					}
				}
			}
		}
	}
doneClaudeCheck:

	configPath := filepath.Join(projectRoot, ".codex", "config.toml")
	if data, err := os.ReadFile(configPath); err == nil {
		var config map[string]interface{}
		if toml.Unmarshal(data, &config) == nil {
			if hooks, ok := config["hooks"].(map[string]interface{}); ok {
				for event := range hooks {
					groups := normalizeHookGroups(hooks[event])
					for _, g := range groups {
						if gm, ok := g.(map[string]interface{}); ok {
							entries, _ := normalizeHookArray(gm["hooks"])
							for _, e := range entries {
								if em, ok := e.(map[string]interface{}); ok {
									if cmd, _ := em["command"].(string); isRegentHookCommand(cmd) {
										fmt.Printf("  %s Codex\n", style.Success(""))
										goto doneCodexCheck
									}
								}
							}
						}
					}
				}
			}
		}
	}
doneCodexCheck:

	opencodeConfig := findOpenCodeConfig(projectRoot)
	if opencodeConfig != "" {
		if data, err := os.ReadFile(opencodeConfig); err == nil {
			cleaned := stripJSONComments(string(data))
			var config map[string]interface{}
			if json.Unmarshal([]byte(cleaned), &config) == nil {
				if plugins, ok := config["plugin"].([]interface{}); ok {
					for _, p := range plugins {
						if s, ok := p.(string); ok && strings.Contains(s, "regent") {
							fmt.Printf("  %s OpenCode\n", style.Success(""))
							break
						}
					}
				}
			}
		}
	}

	if piPackageConfigured(projectRoot) {
		fmt.Printf("  %s Pi\n", style.Success(""))
	}

	fmt.Println()
}

func printManualInstructions(targets []agentTarget) {
	fmt.Println("Manual hook configuration:")
	fmt.Println()
	if hasAgent(targets, agentClaude) {
		fmt.Println("Claude Code .claude/settings.json events:")
		fmt.Println("  UserPromptSubmit -> rgt message-hook user")
		fmt.Println("  Stop             -> rgt message-hook assistant")
		fmt.Println("  PostToolBatch    -> rgt tool-batch-hook")
		fmt.Println()
	}
	if hasAgent(targets, agentCodex) {
		fmt.Println("Codex .codex/config.toml events:")
		fmt.Println("  SessionStart     -> rgt codex-hook")
		fmt.Println("  UserPromptSubmit -> rgt codex-hook")
		fmt.Println("  PostToolUse      -> rgt codex-hook")
		fmt.Println("  Stop             -> rgt codex-hook")
		fmt.Println()
	}
	if hasAgent(targets, agentOpenCode) {
		fmt.Println("OpenCode: copy the re_gent plugin to .opencode/plugins/regent.ts")
		fmt.Println("  The plugin bridges tool.execute.after and session.idle to rgt opencode-hook")
		fmt.Println()
	}
	if hasAgent(targets, agentPi) {
		fmt.Println("Pi project-local package:")
		fmt.Printf("  %s\n", piInstallCommand)
		fmt.Println("  The package forwards Pi events to rgt pi-hook")
		fmt.Println()
	}
}

func createRegentGitignore(projectRoot string) error {
	gitignorePath := filepath.Join(projectRoot, ".regent", ".gitignore")
	// .regent/ is machine-local VCS state (like .git/ itself) — the object
	// store, index, refs, and logs must NOT be committed. The one exception is
	// config.toml: committing it lets teammates who clone inherit the server
	// wiring (url + repo_id) and capture automatically, without running connect.
	// So: ignore everything, then un-ignore config.toml (and this file).
	content := `# re_gent local state — do not commit (like .git/ itself).
# Only config.toml is shared, so teammates inherit the server wiring on clone.
*
!.gitignore
!config.toml
`

	return os.WriteFile(gitignorePath, []byte(content), 0o644)
}

// installBootstrapSkill writes the one skill that teaches an agent how to find
// and install the others, into whichever hosts were actually wired.
//
// Reporting only what was written, and only when something was, follows the
// rule the hook installer already keeps: a run that installed nothing must not
// print a line saying it did.
func installBootstrapSkill(projectRoot string, targets []agentTarget) error {
	var wrote []string
	for _, dir := range skillDirsFor(projectRoot, targets) {
		_, written, err := skills.Install(dir, skills.Bootstrap, false)
		if skills.IsExists(err) {
			// A project-local edit outranks the bundled helper. Re-running init
			// must never silently replace agent instructions the team changed.
			continue
		}
		if err != nil {
			return err
		}
		if written {
			wrote = append(wrote, dir)
		}
	}
	if len(wrote) == 0 {
		return nil
	}
	fmt.Printf("  %s Skills helper installed - ask the agent \"what re_gent skills are available?\"\n", style.Success(""))
	return nil
}

// skillDirsFor maps wired hosts to the directories they load skills from.
func skillDirsFor(projectRoot string, targets []agentTarget) []string {
	var dirs []string
	for _, target := range targets {
		switch target {
		case agentClaude:
			dirs = append(dirs, filepath.Join(projectRoot, ".claude", "skills"))
		case agentCodex:
			dirs = append(dirs, filepath.Join(projectRoot, ".agents", "skills"))
		case agentOpenCode:
			dirs = append(dirs, filepath.Join(projectRoot, ".opencode", "skills"))
		case agentPi:
			dirs = append(dirs, filepath.Join(projectRoot, ".pi", "skills"))
		}
	}
	return dirs
}

func offerSkillInstall(projectRoot string, targets []agentTarget, input *bufio.Reader) error {
	fmt.Printf("Agent skills expose common %s commands inside the agent UI.\n", style.Brand("re_gent"))
	fmt.Println()
	fmt.Println("  log [options]         Show step history")
	fmt.Println("  blame <path>[:<line>] Show line provenance")
	fmt.Println("  show <step>           Show step details")
	fmt.Println()
	fmt.Print(style.Prompt("Install skills?", "[Y/n]:"))

	confirmed, err := confirmedDefaultYes(input)
	if err != nil {
		return fmt.Errorf("read skill confirmation: %w", err)
	}
	if !confirmed {
		fmt.Println()
		fmt.Printf("  %s Skipped - you can install skills manually later\n", style.DimText("-"))
		fmt.Println()
		return nil
	}

	for _, target := range targets {
		switch target {
		case agentClaude:
			if err := installSkills(filepath.Join(projectRoot, ".claude", "skills")); err != nil {
				return err
			}
			fmt.Printf("  %s Claude skills installed in .claude/skills/\n", style.Success(""))
		case agentCodex:
			if err := installSkills(filepath.Join(projectRoot, ".agents", "skills")); err != nil {
				return err
			}
			fmt.Printf("  %s Codex skills installed in .agents/skills/\n", style.Success(""))
		case agentOpenCode:
			if err := installSkills(filepath.Join(projectRoot, ".opencode", "skills")); err != nil {
				return err
			}
			fmt.Printf("  %s OpenCode skills installed in .opencode/skills/\n", style.Success(""))
		case agentPi:
			if err := installSkills(filepath.Join(projectRoot, ".pi", "skills")); err != nil {
				return err
			}
			fmt.Printf("  %s Pi skills installed in .pi/skills/\n", style.Success(""))
		}
	}
	fmt.Println()
	return nil
}

// installSkills writes every shipped skill into skillsDir.
//
// The definitions come from internal/skills, which embeds the real SKILL.md
// files. They used to be Go string literals here, and that is how the shipped
// set drifted to three skills while the repository carried nine: two copies,
// one of them invisible to anyone reading .claude/skills.
func installSkills(skillsDir string) error {
	for _, name := range skills.DefaultNames() {
		_, _, err := skills.Install(skillsDir, name, false)
		if skills.IsExists(err) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func resolveAgentTargets(projectRoot string, target agentTarget) ([]agentTarget, error) {
	switch target {
	case agentClaude:
		return []agentTarget{agentClaude}, nil
	case agentCodex:
		return []agentTarget{agentCodex}, nil
	case agentOpenCode:
		return []agentTarget{agentOpenCode}, nil
	case agentPi:
		return []agentTarget{agentPi}, nil
	case agentBoth:
		return []agentTarget{agentClaude, agentCodex}, nil
	case agentAll:
		return []agentTarget{agentClaude, agentCodex, agentOpenCode, agentPi}, nil
	case agentAuto, "":
		var targets []agentTarget
		if pathExists(filepath.Join(projectRoot, ".claude")) || commandExists("claude") {
			targets = append(targets, agentClaude)
		}
		if pathExists(filepath.Join(projectRoot, ".codex")) || commandExists("codex") {
			targets = append(targets, agentCodex)
		}
		if pathExists(filepath.Join(projectRoot, ".opencode")) || commandExists("opencode") {
			targets = append(targets, agentOpenCode)
		}
		if pathExists(filepath.Join(projectRoot, ".pi")) || commandExists("pi") {
			targets = append(targets, agentPi)
		}
		if len(targets) == 0 {
			targets = append(targets, agentClaude, agentCodex)
		}
		return targets, nil
	default:
		return nil, fmt.Errorf("invalid --agent %q; expected auto, claude, codex, opencode, pi, both, or all", target)
	}
}

func hasAgent(targets []agentTarget, target agentTarget) bool {
	for _, candidate := range targets {
		if candidate == target {
			return true
		}
	}
	return false
}

// confirmedDefaultYes reads a yes/no answer, treating a bare enter as yes.
//
// It delegates to readAnswer rather than reading to a newline itself. This
// package had two confirmation readers that disagreed about what enter looks
// like: readAnswer accepts a carriage return as well, because after a
// full-screen picker hands the terminal back that is what a keypress arrives
// as, and a newline-only read waits forever for a key the user already pressed.
// This function never got that fix, so the skills prompt behind it inherited
// the hang.
//
// An answer that arrives without any terminator is still an answer — the reader
// simply ended — so a partial read is honoured. Only a genuinely empty read is
// an error, which keeps the prompt from silently defaulting to yes when stdin
// is the installing script rather than a person.
func confirmedDefaultYes(input io.Reader) (bool, error) {
	answer, err := readAnswer(input)
	if err != nil && answer == "" {
		return false, err
	}

	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "" || answer == "y" || answer == "yes", nil
}

func backupFile(path string) (string, error) {
	backupPath := path + ".backup"
	return backupPath, os.Rename(path, backupPath)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
