package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

const maxAuthResponseBytes = 64 << 10

// readHiddenInput reads one line from fd without echoing it — a password or
// a personal access token typed at a terminal. It is a package variable
// rather than a direct call to term.ReadPassword purely so a test can
// substitute a fake reader: term.ReadPassword needs a real terminal file
// descriptor, which a test binary's stdin never is.
var readHiddenInput = term.ReadPassword

type authViewer struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	DisplayName   string `json:"display_name"`
	InstanceOwner bool   `json:"instance_owner"`
}

type authMeResponse struct {
	Viewer     authViewer `json:"viewer"`
	AuthMethod string     `json:"auth_method"`
}

type authLoginParams struct {
	serverURL string
	token     string
	client    *http.Client
}

// AuthCmd returns the machine credential command group. Tokens are accepted
// only through a hidden terminal prompt or stdin; the command deliberately has
// no token argument or --token flag because process arguments are observable.
func AuthCmd() *cobra.Command {
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Sign in to a re_gent server and manage this machine's login",
		Args:  cobra.NoArgs,
	}
	auth.AddCommand(authLoginCmd(), authStatusCmd(), authLogoutCmd())
	return auth
}

func authLoginCmd() *cobra.Command {
	var tokenStdin bool
	cmd := &cobra.Command{
		Use:          "login [server-url]",
		Short:        "Validate and store a personal access token",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			serverURL := ""
			if len(args) == 1 {
				serverURL = args[0]
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			serverURL, err = authServerURL(serverURL, cfg)
			if err != nil {
				return err
			}

			client := &http.Client{Timeout: 10 * time.Second}
			// Capability discovery decides the sign-in method, never the
			// hostname (RFC 0001): a server that lists "password" among
			// auth_methods gets the password flow, unless --token-stdin was
			// given (an explicit request for the PAT flow) or stdin is not a
			// terminal (nobody there to type a password); a server that lists
			// "device" and not "password" keeps the device flow; everything
			// else — including a server unreachable or too old to answer
			// capabilities at all — keeps the personal-access-token flow
			// exactly as before.
			caps := remote.FetchCapabilities(cmd.Context(), client, serverURL)
			if !tokenStdin && caps.SupportsAuthMethod("password") && isTerminal(os.Stdin) {
				return runAuthPasswordLogin(cmd, cfg, serverURL)
			}
			if caps.SupportsAuthMethod("device") && !caps.SupportsAuthMethod("password") {
				viewer, err := runAuthDeviceLogin(cmd, cfg, serverURL, client)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Signed in to %s as %s (%s).\n", serverURL, viewer.DisplayName, viewer.Username)
				return nil
			}

			tokenValue, err := readLoginToken(cmd, tokenStdin)
			if err != nil {
				return err
			}
			viewer, err := runAuthLogin(authLoginParams{serverURL: serverURL, token: tokenValue, client: client})
			if err != nil {
				return err
			}
			config.SetCredential(cfg, serverURL, tokenValue)
			cfg.Server.URL = serverURL
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("save login: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Signed in to %s as %s (%s).\n", serverURL, viewer.DisplayName, viewer.Username)
			return nil
		},
	}
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the token from stdin (recommended for automation)")
	return cmd
}

func authStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status [server-url]",
		Short:        "Verify the stored login without displaying its token",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			explicit := ""
			if len(args) == 1 {
				explicit = args[0]
			}
			serverURL, err := authServerURL(explicit, cfg)
			if err != nil {
				return err
			}
			tokenValue := config.TokenForServer(cfg, serverURL)
			if tokenValue == "" {
				return fmt.Errorf("not signed in to %s; run: rgt auth login %s", serverURL, serverURL)
			}
			viewer, err := verifyAuthToken(&http.Client{Timeout: 10 * time.Second}, serverURL, tokenValue)
			if err != nil {
				return err
			}
			role := "member"
			if viewer.InstanceOwner {
				role = "instance owner"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Signed in to %s as %s (%s), %s.\n", serverURL, viewer.DisplayName, viewer.Username, role)
			return nil
		},
	}
}

func authLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "logout [server-url]",
		Short:        "Remove this machine's stored login",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			explicit := ""
			if len(args) == 1 {
				explicit = args[0]
			}
			serverURL, err := authServerURL(explicit, cfg)
			if err != nil {
				return err
			}
			if !config.RemoveCredential(cfg, serverURL) {
				fmt.Fprintf(cmd.OutOrStdout(), "No stored login for %s.\n", serverURL)
				return nil
			}
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("remove login: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Signed out of %s on this machine. Existing server tokens were not revoked.\n", serverURL)
			return nil
		},
	}
}

func runAuthLogin(params authLoginParams) (authViewer, error) {
	serverURL, err := normalizeAuthServerURL(params.serverURL)
	if err != nil {
		return authViewer{}, err
	}
	if strings.TrimSpace(params.token) == "" {
		return authViewer{}, errors.New("token is empty")
	}
	client := params.client
	if client == nil {
		client = http.DefaultClient
	}
	return verifyAuthToken(client, serverURL, strings.TrimSpace(params.token))
}

// maxDeviceLoginWait bounds how long the polling loop below waits when the
// server's own expires_in is missing or absurd, so a malformed response
// cannot hang the command forever.
const maxDeviceLoginWait = 10 * time.Minute

// runAuthDeviceLogin drives the device-login flow (RFC 0004, "CLI flow"):
// start, print the URL and code, poll until approved, store the resulting
// access/refresh pair keyed by server, and verify the identity it was issued
// for exactly as the PAT flow does.
func runAuthDeviceLogin(cmd *cobra.Command, cfg *config.UserConfig, serverURL string, client *http.Client) (authViewer, error) {
	ctx := cmd.Context()
	auth, err := remote.StartDeviceAuthorization(ctx, client, serverURL)
	if err != nil {
		return authViewer{}, fmt.Errorf("start device login: %w", err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "To sign in, open this URL and enter the code:\n\n  %s\n  %s\n\n", auth.VerificationURL, auth.UserCode)
	fmt.Fprintf(out, "Waiting for approval...\n")

	interval := time.Duration(auth.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	wait := time.Duration(auth.ExpiresIn) * time.Second
	if wait <= 0 || wait > maxDeviceLoginWait {
		wait = maxDeviceLoginWait
	}
	deadline := time.Now().Add(wait)

	for {
		if err := sleepOrDone(ctx, interval); err != nil {
			return authViewer{}, err
		}
		if time.Now().After(deadline) {
			return authViewer{}, fmt.Errorf("device login was not approved in time; run `rgt auth login %s` again", serverURL)
		}

		pair, err := remote.PollDeviceToken(ctx, client, serverURL, auth.DeviceCode)
		if err == nil {
			config.SetDeviceCredential(cfg, serverURL, pair.AccessToken, pair.RefreshToken, pair.ExpiresIn)
			cfg.Server.URL = serverURL
			if err := config.Save(cfg); err != nil {
				return authViewer{}, fmt.Errorf("save login: %w", err)
			}
			return verifyAuthToken(client, serverURL, pair.AccessToken)
		}

		var pending *remote.DevicePollError
		if !errors.As(err, &pending) {
			return authViewer{}, fmt.Errorf("poll device login: %w", err)
		}
		switch pending.Code {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "expired":
			return authViewer{}, fmt.Errorf("device code expired before it was approved; run `rgt auth login %s` again", serverURL)
		case "denied":
			return authViewer{}, fmt.Errorf("device login was denied")
		default:
			return authViewer{}, fmt.Errorf("device login: unrecognised state %q", pending.Code)
		}
	}
}

// runAuthPasswordLogin drives the self-hosted password flow (RFC 0005,
// "Teammate flow": "rgt auth login <server>, which prompts for username and
// password... and stores a machine credential. Tokens are never shown to
// people."): prompt for a username and a hidden password, POST
// /api/v1/auth/login, then mint a machine credential through the pre-existing
// PAT-creation route using the session cookie and CSRF the login returned,
// and store that credential exactly as the token-stdin path does.
//
// The credential minted here — not the session — is what gets stored: a
// browser session is short-lived and re-authenticates through the cookie,
// which a CLI invocation started fresh every time does not have. Storing the
// PAT is what makes `rgt auth login` a one-time action instead of something
// that would need to run before every command.
func runAuthPasswordLogin(cmd *cobra.Command, cfg *config.UserConfig, serverURL string) error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return fmt.Errorf("create cookie jar: %w", err)
	}
	client := &http.Client{Timeout: 10 * time.Second, Jar: jar}

	fmt.Fprint(cmd.ErrOrStderr(), "Username: ")
	username, err := readAnswer(cmd.InOrStdin())
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read username: %w", err)
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("username is empty")
	}

	fmt.Fprint(cmd.ErrOrStderr(), "Password: ")
	passwordBytes, err := readHiddenInput(os.Stdin.Fd())
	fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	password := strings.TrimSpace(string(passwordBytes))
	if password == "" {
		return errors.New("password is empty")
	}

	result, err := remote.PasswordLogin(cmd.Context(), client, serverURL, username, password)
	if err != nil {
		return fmt.Errorf("sign in to %s: %w", serverURL, err)
	}
	// The initial admin password (RFC 0005, "Step 0: start the server") signs
	// in successfully but must not be turned into a standing machine
	// credential: onboarding is not finished, and a credential minted now
	// would outlive a password the wizard is about to replace out from under
	// it.
	if result.PasswordChangeRequired {
		return fmt.Errorf("the initial password for %s is still in force; finish setup in the web UI, then run this again", serverURL)
	}

	credentialName := hostname() + " (cli)"
	tokenValue, err := remote.CreateMachineCredential(cmd.Context(), client, serverURL, result.CSRF, credentialName)
	if err != nil {
		return fmt.Errorf("create machine credential: %w", err)
	}

	config.SetCredential(cfg, serverURL, tokenValue)
	cfg.Server.URL = serverURL
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save login: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Signed in as %s on %s.\n", result.User.Username, serverURL)
	return nil
}

// sleepOrDone waits for d, returning early with the context's error if it is
// cancelled first — so a device-login poll loop cannot outlive its caller.
func sleepOrDone(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func verifyAuthToken(client *http.Client, serverURL, tokenValue string) (authViewer, error) {
	if err := remote.ValidateCredentialTransport(serverURL, tokenValue); err != nil {
		return authViewer{}, err
	}
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(serverURL, "/")+"/api/v1/auth/me", nil)
	if err != nil {
		return authViewer{}, fmt.Errorf("build authentication request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+tokenValue)
	response, err := client.Do(request)
	if err != nil {
		return authViewer{}, fmt.Errorf("verify login with %s: %w", serverURL, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAuthResponseBytes+1))
	if err != nil {
		return authViewer{}, fmt.Errorf("read authentication response: %w", err)
	}
	if len(body) > maxAuthResponseBytes {
		return authViewer{}, errors.New("authentication response exceeds size limit")
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return authViewer{}, fmt.Errorf("server rejected the credential; create or rotate a personal access token and retry")
	}
	if response.StatusCode != http.StatusOK {
		return authViewer{}, fmt.Errorf("authentication check failed: server returned %d", response.StatusCode)
	}
	var me authMeResponse
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&me); err != nil {
		return authViewer{}, fmt.Errorf("decode authentication response: %w", err)
	}
	if me.Viewer.ID == "" || me.Viewer.Username == "" {
		return authViewer{}, errors.New("authentication response omitted viewer identity")
	}
	return me.Viewer, nil
}

func readLoginToken(cmd *cobra.Command, tokenStdin bool) (string, error) {
	if tokenStdin {
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 8<<10))
		if err != nil {
			return "", fmt.Errorf("read token from stdin: %w", err)
		}
		if tokenValue := strings.TrimSpace(string(data)); tokenValue != "" {
			return tokenValue, nil
		}
		return "", errors.New("token from stdin is empty")
	}
	if !isTerminal(os.Stdin) {
		return "", errors.New("refusing to read a token from process arguments; pipe it with --token-stdin or run in an interactive terminal")
	}
	fmt.Fprint(cmd.ErrOrStderr(), "Personal access token: ")
	data, err := readHiddenInput(os.Stdin.Fd())
	fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	if tokenValue := strings.TrimSpace(string(data)); tokenValue != "" {
		return tokenValue, nil
	}
	return "", errors.New("token is empty")
}

func authServerURL(explicit string, cfg *config.UserConfig) (string, error) {
	value := explicit
	if value == "" && cfg != nil {
		value = cfg.Server.URL
		if value == "" && cfg.Auth.ServerURL != "" {
			value = cfg.Auth.ServerURL
		}
		if value == "" && len(cfg.Credentials) == 1 {
			value = cfg.Credentials[0].ServerURL
		}
	}
	if value == "" {
		return "", errors.New("no server specified; run: rgt auth login <server-url>")
	}
	return normalizeAuthServerURL(value)
}

func normalizeAuthServerURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid server URL: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("invalid server URL %q: use http:// or https:// with a host", value)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("server URL must not contain credentials, a query, or a fragment")
	}
	return value, nil
}
