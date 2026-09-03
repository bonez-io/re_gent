package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/store"
	"github.com/spf13/cobra"
)

// DisconnectCmd returns the cobra command for `rgt disconnect`.
func DisconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect",
		Short: "Stop capturing this project to a re_gent server",
		Long: "Removes the server wiring and re_gent's agent hooks from this project.\n" +
			"History already on the server is left alone — unwiring one machine must\n" +
			"not erase what the team can see.\n\n" +
			"This is the only way to disconnect a project. It used to also happen as a\n" +
			"side effect of marking an already-connected project in the picker (#28).",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			return reportDisconnect(cwd)
		},
	}
}

// reportDisconnect disconnects a project and prints what changed.
func reportDisconnect(root string) error {
	if err := disconnectProject(root); err != nil {
		return err
	}
	fmt.Printf("  ✓ Removed server wiring\n")
	fmt.Printf("  ✓ Removed Claude Code hooks\n")
	fmt.Printf("  ⚠ Restart any Claude Code / Codex session open in this repo —\n")
	fmt.Printf("    it loaded the hooks at startup and keeps capturing until you do.\n")
	fmt.Printf("  → History already on the server is kept. If the wiring was committed,\n")
	fmt.Printf("    commit its removal too, or teammates who clone are still wired.\n")
	return nil
}

// disconnectProject unwires a project: it clears the remote so nothing is sent,
// and removes re_gent's hooks so nothing is captured. Two deliberate
// non-actions: history already on the server is untouched (one person unwiring
// must not erase the team's record), and other tools' hooks in settings.json are
// preserved — only the entries we added are removed.
func disconnectProject(root string) error {
	regentDir := filepath.Join(root, ".regent")
	if _, err := os.Stat(regentDir); err != nil {
		return fmt.Errorf("%s is not connected", filepath.Base(root))
	}

	s, err := store.Open(regentDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	// readRepoConfig (setup.go) understands both the legacy repo_id binding
	// and the project_id binding RFC 0004 added. s.ReadRepoConfig alone would
	// call a project-id-only binding "not connected", because
	// store.RemoteConfig has no field for project_id — see readRepoConfig's
	// comment for why that gap is closed there instead of here.
	binding, err := readRepoConfig(root)
	if err != nil {
		return fmt.Errorf("read repo config: %w", err)
	}
	if binding.Remote.URL == "" && binding.Remote.RepoID == "" {
		return fmt.Errorf("%s is not connected to a server", filepath.Base(root))
	}

	cfg, err := s.ReadRepoConfig()
	if err != nil {
		return fmt.Errorf("read repo config: %w", err)
	}
	cfg.Remote.URL = ""
	cfg.Remote.RepoID = ""
	if err := s.WriteRepoConfig(cfg); err != nil {
		return fmt.Errorf("write repo config: %w", err)
	}
	// store.WriteRepoConfig above marshals store.RepoConfig, which has no
	// project_id field, so a fresh write already drops it from the file — but
	// only incidentally, by not knowing the key exists. ClearRemoteBinding is
	// the explicit form of the same result (a no-op if there is nothing left
	// to remove), kept here so this does not silently regress if
	// store.WriteRepoConfig's behaviour ever changes to preserve unknown keys.
	_ = config.ClearRemoteBinding(filepath.Join(regentDir, "config.toml"))

	if err := removeClaudeHooks(root); err != nil {
		return fmt.Errorf("remove hooks: %w", err)
	}
	// The Git hook goes with the server binding it delivers to. Only our file
	// is removed, and a hook that was there before us is put back exactly.
	if _, err := removeGitHook(root); err != nil {
		return fmt.Errorf("remove git hook: %w", err)
	}
	// The post-commit hook goes too: disconnect removes re_gent's hooks so
	// nothing is captured, and a baseline refreshing on every commit is still
	// capture.
	if _, err := removePostCommitHook(root); err != nil {
		return fmt.Errorf("remove post-commit hook: %w", err)
	}
	return nil
}

// removeClaudeHooks strips re_gent's hook entries from .claude/settings.json,
// leaving every other hook and setting in place. The file is deleted only when
// nothing of anyone else's remains, so no empty husk is left behind.
func removeClaudeHooks(projectRoot string) error {
	settingsPath := filepath.Join(projectRoot, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to undo
		}
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}

	settings := map[string]interface{}{}
	if err := json.Unmarshal(data, &settings); err != nil {
		// Someone else's malformed file is not ours to rewrite.
		return fmt.Errorf("parse %s: %w", settingsPath, err)
	}

	if hooks, ok := settings["hooks"].(map[string]interface{}); ok {
		for _, event := range []string{"UserPromptSubmit", "Stop", "PostToolBatch", "PostToolUse"} {
			removeRegentHookCommands(hooks, event)
		}
		if len(hooks) == 0 {
			delete(settings, "hooks")
		}
	}

	if len(settings) == 0 {
		if err := os.Remove(settingsPath); err != nil {
			return err
		}
		// Remove .claude too, but only if we left it empty.
		_ = os.Remove(filepath.Join(projectRoot, ".claude"))
		return nil
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, append(out, '\n'), 0o644)
}
