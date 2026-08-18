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
	"github.com/regent-vcs/regent/internal/style"
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
	// noGitHook leaves the Git pre-push hook alone (--no-git-hook). Agent hooks
	// are unaffected: this opts out of sync-on-push, not of capture.
	noGitHook bool
	// out receives diagnostic and exceptional state. Normal onboarding output
	// is rendered by connectHere after the operation succeeds.
	out io.Writer
}

func (p connectParams) writer() io.Writer {
	if p.out != nil {
		return p.out
	}
	return os.Stdout
}

// ConnectCmd returns the cobra command for `rgt connect`.
func ConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect [server-url-or-ssh-target]",
		Short: "Connect this project to a re_gent server and wire agent hooks",
		Long: `Connect this project to a re_gent server.

Run it inside the project you want wired. Run anywhere else it names the fix
and changes nothing, rather than searching the directories below for projects
nobody asked it to touch. Disconnecting is its own command, rgt disconnect.

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
			// Do not contact or provision a host when no project was named. This
			// retains connect's promise to touch nothing outside a project.
			if !isProjectDir(cwd) {
				serverURL, resolveErr := resolveServerURL(explicit)
				if resolveErr != nil {
					return resolveErr
				}
				return notAProject(cwd, serverURL)
			}
			var serverURL string
			if explicit != "" && !isServiceURL(explicit) {
				override, _ := cmd.Flags().GetString("url")
				yes, _ := cmd.Flags().GetBool("yes")
				// This phase is deliberately before isProjectDir/connectHere: an
				// unreachable public URL must never result in a local .regent.
				serverURL, err = prepareMachine(explicit, override, yes, cmd.InOrStdin(), cmd.OutOrStdout(), systemBootstrapper{})
				if err != nil {
					return err
				}
			} else {
				serverURL, err = resolveServerURL(explicit)
				if err != nil {
					return err
				}
				serverURL, err = normalizeServiceURL(serverURL)
				if err != nil {
					return err
				}
				if !(systemBootstrapper{}).Healthy(serverURL) {
					return fmt.Errorf("server %s is unreachable at /healthz; URL form only binds and never provisions", serverURL)
				}
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

			noGitHook, _ := cmd.Flags().GetBool("no-git-hook")

			return connectHere(serverURL, cwd, explicitID, noGitHook, cmd.OutOrStdout(), isTerminal(os.Stdin))
		},
	}
	// Derivation is a guess and it will be wrong for someone: a fork's remote,
	// a monorepo whose subdirectories are separate projects, a checkout with no
	// remote whose derived id is a hash nobody can read. This is how they say
	// otherwise. It is recorded in the binding, so it is said once.
	cmd.Flags().String("as", "", "identity to register this project under, instead of deriving one from its git remote")
	// Sync-on-push is on by default because a queue that outlives an outage
	// should drain at the next moment work is shared, without anyone
	// remembering `rgt sync`. This is the per-run exit; REGENT_GIT_SYNC_ON_PUSH=0
	// is the per-machine one, and `git push --no-verify` is Git's own.
	cmd.Flags().Bool("no-git-hook", false, "do not install the Git pre-push hook that syncs queued history on git push")
	cmd.Flags().String("url", "", "public http(s) URL to prove and bind when provisioning an SSH target")
	cmd.Flags().Bool("yes", false, "provision an SSH target without asking for confirmation")
	return cmd
}

// connectHere wires the one project the user is standing in — the only project
// connect ever touches now that the picker is gone (#28).
//
// It connects and never disconnects. Marking an already-connected project used
// to mean "unwire it", because a tick in a list expresses a state rather than a
// verb; that reading is what made the picker destructive, and it is why
// connecting a project twice used to remove its hooks. `rgt disconnect` is the
// only way to unwire one.
//
// canPrompt is false wherever there is no person to answer — under
// `curl | sh`, in CI, in a devcontainer — and the share question is simply not
// asked there.
func connectHere(serverURL, dir, repoID string, noGitHook bool, out io.Writer, canPrompt bool) error {
	if out == nil {
		out = io.Discard
	}
	flow := style.NewFlow(out)
	flow.Header("connect", filepath.Base(dir))
	var setupOutput bytes.Buffer
	err := flow.Wait("Installing project integration", func() error {
		return runConnect(connectParams{serverURL: serverURL, projectRoot: dir, repoID: repoID, noGitHook: noGitHook, out: &setupOutput})
	})
	if setupOutput.Len() > 0 {
		_, _ = io.Copy(out, &setupOutput)
	}
	if err != nil {
		return fmt.Errorf("%s could not be connected: %w", filepath.Base(dir), err)
	}
	rememberServer(serverURL)
	flow.Step("Server verified")
	flow.Step("Registered with server")
	flow.Step("Agent hooks configured")
	if cfg, err := readRemoteConfig(dir); err == nil {
		flow.Detail("Server", cfg.URL)
		flow.Detail("Project", cfg.RepoID)
	}
	if canPrompt {
		shareWithTeam([]string{dir})
	}
	flow.Complete("Ready to capture")
	flow.Next("Restart your agent here, then run rgt doctor")
	if Verbose() {
		flow.Hint("Team setup: commit .regent/config.toml and .claude/settings.json")
		flow.Hint("Codex teammates run rgt init --agent codex on their own machine")
	}
	flow.End()
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
			Verbosef(p.writer(), "warning: could not create .regent/.gitignore: %v\n", err)
		}
		Verbosef(p.writer(), "  initialized .regent/\n")
	} else {
		s, err = store.Open(regentDir)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		Verbosef(p.writer(), "  using existing .regent/\n")
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
			Verbosef(p.writer(), "  already connected to %s (repo_id: %s)\n", p.serverURL, repoCfg.Remote.RepoID)
			fmt.Fprintf(p.writer(), "  %s Already connected to this server\n", style.Success(""))
			return connectWireHooksTo(p.projectRoot, p.noGitHook, p.writer())
		}
		fmt.Fprintf(p.writer(), "  %s %s has no record of project %s; registering it again.\n", style.Warning(""),
			p.serverURL, repoCfg.Remote.RepoID)

	case repoCfg.Remote.URL != "" && repoCfg.Remote.RepoID != "":
		// Moving to a different server. This is a move, not a disconnect: the
		// hooks stay, capture never stops. Naming the old server is the only
		// warning the user gets that their history did not travel with them.
		fmt.Fprintf(p.writer(), "  %s Moving this project from %s to %s.\n", style.Warning(""), repoCfg.Remote.URL, p.serverURL)
		fmt.Fprintf(p.writer(), "    History already on %s stays there; it is not copied.\n", repoCfg.Remote.URL)
	}

	// 4. Register the repo with the server.
	repoID, err := registerRepo(p.serverURL, token, p.projectRoot, p.repoID, p.httpClient)
	if err != nil {
		return fmt.Errorf("register repo: %w", err)
	}
	Verbosef(p.writer(), "  registered repo_id: %s\n", repoID)

	// 5. Write remote config to .regent/config.toml.
	repoCfg.Remote.URL = p.serverURL
	repoCfg.Remote.RepoID = repoID
	if err := s.WriteRepoConfig(repoCfg); err != nil {
		return fmt.Errorf("write repo config: %w", err)
	}
	Verbosef(p.writer(), "  wrote remote config\n")

	// 6. Carry over history recorded before this moment.
	//
	// The binding just written moves every read to a machine-local cache keyed
	// to this server. Without this step, everything captured before now stays in
	// the project's own .regent/ where nothing reads it, nothing uploads it and
	// nothing mentions it — `rgt log --session <id>` exits 1 for a session that
	// worked a minute ago. See carryover.go.
	var carryover bytes.Buffer
	carryOverLocalHistory(&carryover, s, carryOverConfig(p, repoID, token))
	if Verbose() || strings.Contains(carryover.String(), carriedOverHeadline) || strings.Contains(carryover.String(), "⚠") {
		_, _ = io.Copy(p.writer(), &carryover)
	}

	// 7. Wire Claude hooks (merge/dedupe).
	return connectWireHooksTo(p.projectRoot, p.noGitHook, p.writer())
}

// connectWireHooks wires every agent detected in the project, not just Claude.
//
// It used to install the Claude hook alone, which silently left Codex,
// OpenCode and Pi uncaptured for anyone onboarding through the team server.
// That became a hard failure once the installer started verifying its own
// work: rgt doctor correctly reports the missing hook, the installer treats
// that as failure, and the pasted command dies on any machine with a second
// agent installed. Found by running the installer against a real server.
func connectWireHooks(projectRoot string, noGitHook bool) error {
	return connectWireHooksTo(projectRoot, noGitHook, os.Stdout)
}

func connectWireHooksTo(projectRoot string, noGitHook bool, out io.Writer) error {
	targets, err := resolveAgentTargets(projectRoot, agentAuto)
	if err != nil {
		return err
	}

	installed, err := wireAgentsTo(projectRoot, targets, out)
	if err != nil {
		return err
	}
	if len(installed) == 0 {
		return fmt.Errorf("no agent hooks were configured in %s", projectRoot)
	}
	// The Git hook is wired after the agent hooks and after the "no agents"
	// verdict above, on purpose: it delivers what the agent hooks capture, so
	// it is meaningless without them, and it must not turn an unwired project
	// into one that reports success. Its own failure is not fatal either — a
	// project that captures but does not sync on push is degraded, not broken,
	// and doctor will name it.
	if !noGitHook {
		if outcome, err := wireGitHook(projectRoot); err != nil {
			Verbosef(out, "  Git pre-push hook not configured: %v\n", err)
		} else {
			reportGitHookWiredTo(out, outcome)
			reportGitHookSkippedTo(out, outcome)
		}
	}
	// Agents read their hook config at session startup, so a session that was
	// already running when this ran won't capture until it is restarted. This
	// is the single most common "why wasn't my change captured?" cause.
	Verbosef(out, "  restart any agent session already open in this repo; hooks load at startup\n")
	// What this claims and what a clone actually does have to be the same thing.
	// The hook written into .claude/settings.json names this machine's rgt and
	// falls back to whatever `rgt` PATH resolves (see sharedHookCommand), so the
	// teammate's requirement is not "installed" but "on PATH" — and if it is
	// not, `rgt doctor` names the missing binary rather than leaving them to
	// discover the silence. Saying "installed" was the shorter sentence and the
	// wrong one (#23).
	Verbosef(out, "  commit .regent/config.toml and .claude/settings.json to auto-wire teammates\n")
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
