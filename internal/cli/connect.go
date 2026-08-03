package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/regent-vcs/regent/internal/config"
	"github.com/regent-vcs/regent/internal/index"
	"github.com/regent-vcs/regent/internal/store"
	"github.com/spf13/cobra"
)

// ErrNotSignedIn is returned when no auth token is found in the global config.
var ErrNotSignedIn = fmt.Errorf("not signed in\n\nRun: rgt login <server-url>")

// connectParams bundles everything runConnect needs; injectable for testing.
type connectParams struct {
	serverURL   string
	projectRoot string
	token       string // when set, stored globally before registering (one-shot connect)
	configPath  string // global config path; "" means default
	httpClient  *http.Client
}

// ConnectCmd returns the cobra command for `rgt connect`.
func ConnectCmd() *cobra.Command {
	var token string
	cmd := &cobra.Command{
		Use:   "connect <server-url>",
		Short: "Register this repo with a re_gent server and wire Claude hooks",
		Long: `Register this repo with a remote re_gent server.

connect checks that you are signed in (see: rgt login), registers the repo to
obtain a repo_id, writes the remote URL and repo_id to .regent/config.toml, and
installs Claude Code hooks. Running connect more than once is safe: hooks are
merged rather than duplicated and existing config is preserved.`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		Annotations: map[string]string{
			"commandOrder": "1",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			return runConnect(connectParams{
				serverURL:   strings.TrimRight(args[0], "/"),
				projectRoot: cwd,
				token:       token,
			})
		},
	}
	cmd.Flags().StringVar(&token, "token", "",
		"auth token to store before registering, so a separate 'rgt login' is not needed; use a long random value")
	return cmd
}

func runConnect(p connectParams) error {
	// 0. If a token was supplied, store it globally first — same as `rgt login` —
	//    so a single `connect --token` both authenticates and registers.
	if p.token != "" {
		fmt.Println("  ! --token was passed on the command line; it may be saved in your shell history")
		authCfg := &config.UserConfig{Auth: config.Auth{
			ServerURL: p.serverURL,
			Token:     strings.TrimSpace(p.token),
		}}
		if err := config.CheckAuth(authCfg); err != nil {
			return fmt.Errorf("invalid --token: %w", err)
		}
		save := config.Save
		if p.configPath != "" {
			save = func(c *config.UserConfig) error { return config.SaveTo(p.configPath, c) }
		}
		if err := save(authCfg); err != nil {
			return fmt.Errorf("store auth token: %w", err)
		}
		fmt.Printf("  ✓ Stored auth token for %s\n", p.serverURL)
	}

	// 1. Load global user config and verify the user is signed in.
	var userCfg *config.UserConfig
	var err error
	if p.configPath == "" {
		userCfg, err = config.Load()
	} else {
		userCfg, err = config.LoadFrom(p.configPath)
	}
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if userCfg.Auth.Token == "" {
		return ErrNotSignedIn
	}
	token := userCfg.Auth.Token

	// 2. Initialise .regent/ if it doesn't exist.
	regentDir := filepath.Join(p.projectRoot, ".regent")
	var s *store.Store
	if _, statErr := os.Stat(regentDir); os.IsNotExist(statErr) {
		s, err = store.Init(p.projectRoot)
		if err != nil {
			return fmt.Errorf("init store: %w", err)
		}
		idx, err := index.Open(s)
		if err != nil {
			return fmt.Errorf("init index: %w", err)
		}
		_ = idx.Close()
		if err := createRegentGitignore(p.projectRoot); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not create .regent/.gitignore: %v\n", err)
		}
		fmt.Printf("  ✓ Initialized .regent/\n")
	} else {
		s, err = store.Open(regentDir)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		fmt.Printf("  - .regent/ already exists\n")
	}

	// 3. Check whether this repo is already connected to this server.
	repoCfg, err := s.ReadRepoConfig()
	if err != nil {
		return fmt.Errorf("read repo config: %w", err)
	}
	if repoCfg.Remote.URL == p.serverURL && repoCfg.Remote.RepoID != "" {
		fmt.Printf("  - Already connected to %s (repo_id: %s)\n", p.serverURL, repoCfg.Remote.RepoID)
		return connectWireHooks(p.projectRoot)
	}

	// 4. Register the repo with the server.
	repoID, err := registerRepo(p.serverURL, token, p.projectRoot, p.httpClient)
	if err != nil {
		return fmt.Errorf("register repo: %w", err)
	}
	fmt.Printf("  ✓ Registered (repo_id: %s)\n", repoID)

	// 5. Write remote config to .regent/config.toml.
	repoCfg.Remote.URL = p.serverURL
	repoCfg.Remote.RepoID = repoID
	if err := s.WriteRepoConfig(repoCfg); err != nil {
		return fmt.Errorf("write repo config: %w", err)
	}
	fmt.Printf("  ✓ Wrote remote config\n")

	// 6. Wire Claude hooks (merge/dedupe).
	return connectWireHooks(p.projectRoot)
}

func connectWireHooks(projectRoot string) error {
	result, err := installClaudeHook(projectRoot)
	if err != nil {
		return fmt.Errorf("install Claude hooks: %w", err)
	}
	if result.BackupPath != "" {
		fmt.Fprintf(os.Stderr, "warning: backed up invalid hook config to %s\n", result.BackupPath)
	}
	fmt.Printf("  ✓ Claude Code hooks configured\n")
	// Agents read their hook config at session startup, so a session that was
	// already running when this ran won't capture until it is restarted. This
	// is the single most common "why wasn't my change captured?" cause.
	fmt.Printf("  ⚠ Restart any Claude Code / Codex session already open in this repo —\n")
	fmt.Printf("    agents load hooks at startup, so a running session won't capture until\n")
	fmt.Printf("    you restart it. (New sessions, and teammates who clone, are unaffected.)\n")
	fmt.Printf("  → To auto-wire teammates: commit .regent/config.toml and .claude/settings.json\n")
	fmt.Printf("    (the rest of .regent/ is git-ignored). Then a clone + `rgt` installed is all\n")
	fmt.Printf("    they need — no connect step.\n")
	return nil
}

// registerRepo POSTs to <serverURL>/repos and returns the assigned repo_id.
// A 401/403 response is converted to ErrNotSignedIn.
func registerRepo(serverURL, token, projectRoot string, client *http.Client) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}

	repoID := deriveRepoID(projectRoot)
	body, _ := json.Marshal(map[string]string{"repo_id": repoID})
	req, err := http.NewRequest(http.MethodPost, serverURL+"/repos", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("POST %s/repos: %w", serverURL, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// parse body below
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", ErrNotSignedIn
	default:
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var result struct {
		RepoID string `json:"repo_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if result.RepoID == "" {
		return "", fmt.Errorf("server returned empty repo_id")
	}
	return result.RepoID, nil
}

// deriveRepoID turns a project directory into a server-legal repo id: lowercase,
// characters restricted to [a-z0-9._-], starting with an alphanumeric, max 64
// bytes. Mirrors the server's repoIDRE (^[a-z0-9][a-z0-9._-]{0,63}$) so a repo
// registers under a stable, human-readable name derived from its folder.
func deriveRepoID(projectRoot string) string {
	base := strings.ToLower(filepath.Base(projectRoot))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	id := strings.TrimLeft(b.String(), "._-")
	if len(id) > 64 {
		id = id[:64]
	}
	if id == "" {
		return "repo"
	}
	if reservedRepoIDs[id] {
		// A folder literally named e.g. "repos"/"aux"/"nul" derives an id the
		// server rejects as reserved; suffix it so connect doesn't 400.
		return id + "-repo"
	}
	return id
}

// reservedRepoIDs mirrors the server's reserved set (internal/server/server.go):
// the "repos" registry path plus Windows reserved device names. A derived id
// that lands on one of these would be rejected by the server, so deriveRepoID
// steers around them.
var reservedRepoIDs = map[string]bool{
	"repos": true, "con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}
