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
	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/store"
	"github.com/spf13/cobra"
)

// ErrNotSignedIn is returned when no auth token is found in the global config.
var ErrNotSignedIn = fmt.Errorf("the server refused this request as unauthenticated,\nand rgt has no sign-in command to fix that with.\n\nThis server is configured to require credentials; ask whoever runs it.")

// connectParams bundles everything runConnect needs; injectable for testing.
type connectParams struct {
	serverURL   string
	projectRoot string
	configPath  string // global config path; "" means default
	httpClient  *http.Client
	// repoID overrides the identity that would otherwise be derived from the
	// project's git remote. Empty means derive.
	repoID string
}

// ConnectCmd returns the cobra command for `rgt connect`.
func ConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect [server-url]",
		Short: "Connect projects to a re_gent server and wire agent hooks",
		Long: `Connect this project — or several — to a re_gent server.

Run inside a project and connect wires that project. Run it anywhere else and
connect offers the projects it finds below, so wiring several does not mean
cd-ing into each one. Selecting a project that is already connected
disconnects it.

The server URL is remembered after the first successful run, so later runs can
omit it. Running connect more than once is safe: hooks are merged rather than
duplicated and existing config is preserved.

connect replaces setup, which did the same job with different answers.`,
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		Annotations: map[string]string{
			"commandOrder": "1",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			// The URL is optional because setup's was: a machine that has
			// connected once already knows its server, and making connect
			// demand the argument again would have been a regression dressed
			// up as a simplification.
			explicit := ""
			if len(args) > 0 {
				explicit = args[0]
			}
			serverURL, err := resolveServerURL(explicit)
			if err != nil {
				return err
			}

			// An identity supplied here is checked before anything is written
			// or registered. A name the server will reject must fail with the
			// name in the message, not as a 400 halfway through connecting.
			explicitID, _ := cmd.Flags().GetString("as")
			if explicitID != "" {
				if err := remote.ValidateRepoID(explicitID); err != nil {
					return fmt.Errorf("cannot use %q as this project's identity: %w", explicitID, err)
				}
			}

			// Standing inside a project connects it — the common case, and the
			// one the installer takes.
			if isProjectDir(cwd) {
				return connectHere(serverURL, cwd, explicitID, cmd.OutOrStdout(), isTerminal(os.Stdin))
			}
			// Otherwise offer the projects below here.
			return connectPicked(serverURL, cwd, cmd.OutOrStdout(), isTerminal(os.Stdin), chooseWithPicker, shareWithTeam)
		},
	}
	// Derivation is a guess and it will be wrong for someone: a fork's remote,
	// a monorepo whose subdirectories are separate projects, a checkout with no
	// remote whose derived id is a hash nobody can read. This is how they say
	// otherwise. It is recorded in the binding, so it is said once.
	cmd.Flags().String("as", "", "identity to register this project under, instead of deriving one from its git remote")
	return cmd
}

// connectHere wires the one project the user is standing in.
//
// It goes through applyConnections rather than calling runConnect directly so
// that the single-project and multi-project routes cannot drift: the toggle
// behaviour, the per-project reporting, and the share offer are the same code
// on both.
func connectHere(serverURL, dir, repoID string, out io.Writer, canPrompt bool) error {
	share := shareWithTeam
	if !canPrompt {
		share = nil
	}
	wired, unwired, failed := applyConnections(serverURL, []string{dir}, out, false, repoID)
	if failed > 0 {
		return fmt.Errorf("%s could not be connected", filepath.Base(dir))
	}
	if len(wired) > 0 {
		rememberServer(serverURL)
		if share != nil {
			share(wired)
		}
	}
	if unwired > 0 {
		fmt.Fprintf(out, "\n%s disconnected.\n", filepath.Base(dir))
	}
	return nil
}

func runConnect(p connectParams) error {
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
	// A token is OPTIONAL: the default server is open, and requiring sign-in
	// here broke the advertised onboarding ("install rgt, then rgt connect
	// <server>") on every machine that had never run `rgt login`. Send a stored
	// token when there is one and let a server that genuinely requires auth
	// answer 401 — registerRepo turns that into ErrNotSignedIn.
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
	switch {
	case repoCfg.Remote.URL == p.serverURL && repoCfg.Remote.RepoID != "":
		// Local config is a claim about the server, not proof of it. A server
		// restored from backup, wiped, or replaced by a different deployment at
		// the same address has no record of this project — and trusting the
		// file would leave every future upload rejected for an unknown repo,
		// with nothing on screen to say so.
		if serverKnowsRepo(p.serverURL, repoCfg.Remote.RepoID, p.httpClient, token) {
			fmt.Printf("  - Already connected to %s (repo_id: %s)\n", p.serverURL, repoCfg.Remote.RepoID)
			return connectWireHooks(p.projectRoot)
		}
		fmt.Printf("  ⚠ %s has no record of this project (repo_id: %s) — re-registering it.\n",
			p.serverURL, repoCfg.Remote.RepoID)

	case repoCfg.Remote.URL != "" && repoCfg.Remote.RepoID != "":
		// Moving to a different server. This is a move, not a disconnect: the
		// hooks stay, capture never stops. Naming the old server is the only
		// warning the user gets that their history did not travel with them.
		fmt.Printf("  → Moving this project from %s to %s.\n", repoCfg.Remote.URL, p.serverURL)
		fmt.Printf("    History already on %s stays there — it is not copied across.\n", repoCfg.Remote.URL)
	}

	// 4. Register the repo with the server.
	repoID, err := registerRepo(p.serverURL, token, p.projectRoot, p.repoID, p.httpClient)
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

	// 6. Carry over history recorded before this moment.
	//
	// The binding just written moves every read to a machine-local cache keyed
	// to this server. Without this step, everything captured before now stays in
	// the project's own .regent/ where nothing reads it, nothing uploads it and
	// nothing mentions it — `rgt log --session <id>` exits 1 for a session that
	// worked a minute ago. See carryover.go.
	carryOverLocalHistory(os.Stdout, s, carryOverConfig(p, repoID, token))

	// 7. Wire Claude hooks (merge/dedupe).
	return connectWireHooks(p.projectRoot)
}

// connectWireHooks wires every agent detected in the project, not just Claude.
//
// It used to install the Claude hook alone, which silently left Codex,
// OpenCode and Pi uncaptured for anyone onboarding through the team server.
// That became a hard failure once the installer started verifying its own
// work: rgt doctor correctly reports the missing hook, the installer treats
// that as failure, and the pasted command dies on any machine with a second
// agent installed. Found by running the installer against a real server.
func connectWireHooks(projectRoot string) error {
	targets, err := resolveAgentTargets(projectRoot, agentAuto)
	if err != nil {
		return err
	}

	installed, err := wireAgents(projectRoot, targets)
	if err != nil {
		return err
	}
	if len(installed) == 0 {
		return fmt.Errorf("no agent hooks were configured in %s", projectRoot)
	}
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

// serverKnowsRepo asks the server whether it still has a record of this repo.
//
// A failure to reach the server, or anything unexpected in the reply, returns
// true — "assume it knows". The alternative is worse in exactly the case that
// matters: on a flaky network or a server mid-restart, returning false would
// re-register a project that was fine, on every connect, forever.
func serverKnowsRepo(serverURL, repoID string, client *http.Client, token string) bool {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(serverURL, "/")+"/repos", nil)
	if err != nil {
		return true
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return true
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return true
	}
	var body struct {
		Repos []string `json:"repos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return true
	}
	for _, r := range body.Repos {
		if r == repoID {
			return true
		}
	}
	return false
}

// registerRepo POSTs to <serverURL>/repos and returns the assigned repo_id.
// A 401/403 response is converted to ErrNotSignedIn.
// requested, when non-empty, is an identity the user named explicitly and which
// has already been validated; otherwise one is derived from the project.
func registerRepo(serverURL, token, projectRoot, requested string, client *http.Client) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}

	repoID := requested
	if repoID == "" {
		repoID = deriveRepoID(projectRoot)
	}
	body, _ := json.Marshal(map[string]string{"repo_id": repoID})
	req, err := http.NewRequest(http.MethodPost, serverURL+"/repos", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Only send Authorization when we actually have a token; a bare "Bearer "
	// is malformed and a strict server would reject it.
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

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
