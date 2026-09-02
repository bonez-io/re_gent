package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/store"
	"github.com/bonez-io/re_gent/internal/style"
	"github.com/spf13/cobra"
)

// ErrNotSignedIn is returned when no auth token is found in the global config.
var ErrNotSignedIn = fmt.Errorf("the server refused this request as unauthenticated\n\nSign in, then retry:\n\n  rgt auth login <server-url>")

// connectParams bundles everything runConnect needs; injectable for testing.
type connectParams struct {
	serverURL   string
	projectRoot string
	configPath  string // global config path; "" means default
	httpClient  *http.Client
	// repoID overrides the identity that would otherwise be derived from the
	// project's git remote (legacy server), or the display name that would
	// otherwise be derived from the fingerprint's remote (project-id server,
	// RFC 0004). Empty means derive.
	repoID string
	// noGitHook leaves the Git pre-push hook alone (--no-git-hook). Agent hooks
	// are unaffected: this opts out of sync-on-push, not of capture.
	noGitHook bool
	// agent selects which host integrations to wire. Empty retains auto-detection.
	agent agentTarget
	// org selects the organization enrollment route on a project-id server
	// (RFC 0004). Ignored against a legacy server.
	org string
	// asFork accepts enrolling a detected fork as its own project. Without
	// it, a fork match stops connect rather than silently picking one of the
	// two choices RFC 0004 describes.
	asFork bool
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

			// The identity/display name supplied here is validated once
			// runConnect knows which server it is talking to: against a
			// legacy server it is still a repo_id, constrained to that
			// charset, and gets checked before anything is written; against a
			// server with the project API it is a free-text display name (RFC
			// 0004), which repo_id's charset would wrongly reject ("Payments
			// API" has a space and an uppercase letter). See runConnectLegacy
			// and runConnectProject.
			explicitID, _ := cmd.Flags().GetString("as")

			noGitHook, _ := cmd.Flags().GetBool("no-git-hook")
			agent, _ := cmd.Flags().GetString("agent")
			if _, err := resolveAgentTargets(cwd, agentTarget(agent)); err != nil {
				return err
			}
			org, _ := cmd.Flags().GetString("org")
			asFork, _ := cmd.Flags().GetBool("as-fork")

			return connectHere(serverURL, cwd, explicitID, noGitHook, agentTarget(agent), org, asFork, cmd.OutOrStdout(), isTerminal(os.Stdin))
		},
	}
	// Derivation is a guess and it will be wrong for someone: a fork's remote,
	// a monorepo whose subdirectories are separate projects, a checkout with no
	// remote whose derived id is a hash nobody can read. This is how they say
	// otherwise. It is recorded in the binding, so it is said once.
	cmd.Flags().String("as", "", "identity (legacy server) or display name (project-id server) for this project, instead of deriving one")
	// Sync-on-push is on by default because a queue that outlives an outage
	// should drain at the next moment work is shared, without anyone
	// remembering `rgt sync`. This is the per-run exit; REGENT_GIT_SYNC_ON_PUSH=0
	// is the per-machine one, and `git push --no-verify` is Git's own.
	cmd.Flags().Bool("no-git-hook", false, "do not install the Git pre-push hook that syncs queued history on git push")
	cmd.Flags().String("agent", string(agentAuto), "Agent hooks to configure: auto, claude, codex, opencode, pi, both, all")
	cmd.Flags().String("url", "", "public http(s) URL to prove and bind when provisioning an SSH target")
	cmd.Flags().Bool("yes", false, "provision an SSH target without asking for confirmation")
	// --org selects the organization route (RFC 0004) on a server that has
	// adopted the project API. Meaningless, and ignored, against a legacy
	// server: there is nothing for it to select there.
	cmd.Flags().String("org", "", "organization to enroll this project in (project-id servers only)")
	// The default for a detected fork is to explain the two choices and do
	// nothing, because only one of them — contribute to the upstream project —
	// is implemented, and it is not this one. --as-fork accepts the other
	// choice explicitly: enroll it as your own project.
	cmd.Flags().Bool("as-fork", false, "enroll a detected fork as its own project instead of stopping to ask")
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
func connectHere(serverURL, dir, repoID string, noGitHook bool, agent agentTarget, org string, asFork bool, out io.Writer, canPrompt bool) error {
	if out == nil {
		out = io.Discard
	}
	flow := style.NewFlow(out)
	flow.Header("connect", filepath.Base(dir))
	var setupOutput bytes.Buffer
	err := flow.Wait("Installing project integration", func() error {
		return runConnect(connectParams{serverURL: serverURL, projectRoot: dir, repoID: repoID, noGitHook: noGitHook, agent: agent, org: org, asFork: asFork, out: &setupOutput})
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
	// <server>") on every machine that had never run `rgt auth login`. Send a stored
	// token when there is one and let a server that genuinely requires auth
	// answer 401 — registerRepo turns that into ErrNotSignedIn.
	token := config.TokenForServer(userCfg, p.serverURL)
	if err := remote.ValidateCredentialTransport(p.serverURL, token); err != nil {
		return err
	}

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

	// 2.5. Discover what the server supports (RFC 0004). This never fails
	// connect on its own: a server that predates capabilities, or one that is
	// simply unreachable right now, is legacy, and runConnectLegacy is
	// exactly what every server behaved as before this existed. Only a
	// server that explicitly lists "project_ids" gets the new flow.
	caps := remote.FetchCapabilities(context.Background(), p.httpClient, p.serverURL)
	if caps.HasFeature("project_ids") {
		return runConnectProject(p, s, token)
	}
	return runConnectLegacy(p, s, token)
}

// runConnectLegacy is every server's behaviour before RFC 0004: a
// client-derived repo_id, registered with POST /repos, bound in
// .regent/config.toml as [remote].repo_id.
func runConnectLegacy(p connectParams, s *store.Store, token string) error {
	if p.repoID != "" {
		// Checked here rather than at flag-parse time: against a project-id
		// server this same flag is a free-text display name, so the
		// repo_id charset restriction only makes sense once we know this is
		// the legacy path. See ConnectCmd's "as" flag.
		if err := remote.ValidateRepoID(p.repoID); err != nil {
			return fmt.Errorf("cannot use %q as this project's identity: %w", p.repoID, err)
		}
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
			return connectWireHooksForTargetTo(p.projectRoot, p.noGitHook, p.agent, p.writer())
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
	return connectWireHooksForTargetTo(p.projectRoot, p.noGitHook, p.agent, p.writer())
}

// runConnectProject is RFC 0004's "connect once": the server has the project
// API, so identity comes from a computed source fingerprint rather than a
// derived string, and the binding is a project_id rather than a repo_id.
//
// Unlike runConnectLegacy, the binding is written last: stage the cache,
// import existing history, confirm it landed, and only then write
// .regent/config.toml (issue #45's atomic cutover). A failure at any earlier
// point leaves the previous binding — none, or a different server's — exactly
// as it was, per RFC 0001 step 6: "A failure before this point leaves the
// previous capture mode active."
func runConnectProject(p connectParams, s *store.Store, token string) error {
	ctx := context.Background()
	regentConfigPath := filepath.Join(p.projectRoot, ".regent", "config.toml")

	// Already bound to a project on this server? Confirm with the server
	// instead of trusting the file, and stop: this is the no-op re-run RFC
	// 0004 requires ("must GET the project, confirm it exists, and be a
	// no-op on success").
	binding, err := config.LoadRemoteBinding(regentConfigPath)
	if err != nil {
		return fmt.Errorf("read repo config: %w", err)
	}
	switch {
	case binding.ProjectID != "" && binding.URL == p.serverURL:
		project, getErr := remote.GetProject(ctx, p.httpClient, p.serverURL, token, binding.ProjectID)
		if getErr == nil {
			Verbosef(p.writer(), "  already connected to %s (project_id: %s)\n", p.serverURL, binding.ProjectID)
			fmt.Fprintf(p.writer(), "  %s already enrolled as %q, attaching\n", style.Success(""), project.DisplayName)
			return connectWireHooksForTargetTo(p.projectRoot, p.noGitHook, p.agent, p.writer())
		}
		if remote.IsNotSignedIn(getErr) {
			return ErrNotSignedIn
		}
		fmt.Fprintf(p.writer(), "  %s %s has no record of project %s; enrolling again.\n", style.Warning(""), p.serverURL, binding.ProjectID)
	case binding.URL != "" && binding.URL != p.serverURL:
		// Moving to a different server. This is a move, not a disconnect: the
		// hooks stay, capture never stops. Naming the old server is the only
		// warning the user gets that their history did not travel with them.
		fmt.Fprintf(p.writer(), "  %s Moving this project from %s to %s.\n", style.Warning(""), binding.URL, p.serverURL)
		fmt.Fprintf(p.writer(), "    History already on %s stays there; it is not copied.\n", binding.URL)
	}

	// Compute the fingerprint and pick a display name. A directory that is
	// not a git repository has no fingerprint at all (RFC 0004): it is always
	// a new project, named from --as when given, else from the folder — the
	// same fallback a git repository with no remote uses below, and the same
	// one deriveRepoID always used for a non-git directory. It still
	// connects; it is just told, once, that nothing will find its way back
	// to this exact project from a different checkout, because there is no
	// fingerprint for a second checkout to match.
	fp, hasFingerprint := sourceFingerprint(p.projectRoot)
	if !hasFingerprint {
		fmt.Fprintf(p.writer(), "  %s\n", style.DimText(fmt.Sprintf(
			"%s is not a git repository, so it has no source fingerprint; a different checkout will not attach to this project automatically.",
			filepath.Base(p.projectRoot))))
	}
	displayName := p.repoID
	if displayName == "" && fp.Remote != "" {
		displayName = lastPathSegment(fp.Remote)
	}
	if displayName == "" {
		displayName = filepath.Base(p.projectRoot)
	}

	req := remote.EnrollProjectRequest{Org: p.org, DisplayName: displayName}
	if hasFingerprint {
		req.Fingerprint = fp.Hex
		req.Remote = fp.Remote
		req.RootCommit = fp.RootCommit
	}

	result, err := remote.EnrollProject(ctx, p.httpClient, p.serverURL, token, req)
	if err != nil {
		if remote.IsNotSignedIn(err) {
			return ErrNotSignedIn
		}
		if remote.IsFingerprintConflict(err) {
			return fmt.Errorf("%s is already enrolled in this organization, and you do not have access to it; ask an admin to add you", filepath.Base(p.projectRoot))
		}
		return fmt.Errorf("enroll project: %w", err)
	}

	// A fork of a public project: RFC 0004 defaults to offering to contribute
	// to the upstream instead of enrolling a new project, and contributing is
	// not implemented. Rather than pretend otherwise, stop and say so plainly
	// unless the caller has explicitly chosen to enroll the fork on its own.
	if result.Upstream != nil && !p.asFork {
		orgFlag := ""
		if p.org != "" {
			orgFlag = " --org " + p.org
		}
		return fmt.Errorf(`%s looks like a fork of %q (%s).

Two choices:
  - enroll it as your own project:
      rgt connect %s%s --as-fork
  - contribute your sessions to %q instead:
      not implemented yet

Nothing was written.`, filepath.Base(p.projectRoot), result.Upstream.DisplayName, result.Upstream.ID, p.serverURL, orgFlag, result.Upstream.DisplayName)
	}

	if result.Created {
		Verbosef(p.writer(), "  enrolled project: %s (%s)\n", result.Project.DisplayName, result.Project.ID)
	} else {
		// The connect-once guarantee: this fingerprint was already enrolled,
		// by this machine or another clone of the same repository, and the
		// server handed back the existing project instead of a duplicate.
		fmt.Fprintf(p.writer(), "  %s already enrolled as %q, attaching\n", style.Success(""), result.Project.DisplayName)
	}

	// Carry over history recorded before this moment, BEFORE writing the
	// binding: the binding is the thing that says "the server is now
	// canonical", and it must not say that until the history is actually
	// there (#45).
	var carryover bytes.Buffer
	outcome := carryOverLocalHistory(&carryover, s, carryOverConfigForProject(p, result.Project.ID, token))
	if outcome.Failed() {
		_, _ = io.Copy(p.writer(), &carryover)
		return fmt.Errorf("could not carry this project's existing history over to %s; the previous capture mode is unchanged and no binding was written", p.serverURL)
	}
	if Verbose() || strings.Contains(carryover.String(), carriedOverHeadline) || strings.Contains(carryover.String(), "⚠") {
		_, _ = io.Copy(p.writer(), &carryover)
	}

	// Only now write the binding.
	if err := config.SaveRemoteBinding(regentConfigPath, config.RemoteBinding{URL: p.serverURL, ProjectID: result.Project.ID}); err != nil {
		return fmt.Errorf("write repo config: %w", err)
	}
	Verbosef(p.writer(), "  wrote remote config\n")

	return connectWireHooksForTargetTo(p.projectRoot, p.noGitHook, p.agent, p.writer())
}

// lastPathSegment returns the part of a "host/path" fingerprint remote after
// its final "/", used as the default display name for an enrolled project —
// RFC 0004: "Display name defaults to --as or the last path segment of the
// remote."
func lastPathSegment(remote string) string {
	remote = strings.TrimSuffix(remote, "/")
	if i := strings.LastIndexByte(remote, '/'); i >= 0 {
		return remote[i+1:]
	}
	return remote
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
	return connectWireHooksForTargetTo(projectRoot, noGitHook, agentAuto, out)
}

func connectWireHooksForTargetTo(projectRoot string, noGitHook bool, target agentTarget, out io.Writer) error {
	targets, err := resolveAgentTargets(projectRoot, target)
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
