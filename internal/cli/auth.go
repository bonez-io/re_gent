package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
			tokenValue, err := readLoginToken(cmd, tokenStdin)
			if err != nil {
				return err
			}
			viewer, err := runAuthLogin(authLoginParams{serverURL: serverURL, token: tokenValue, client: &http.Client{Timeout: 10 * time.Second}})
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
	data, err := term.ReadPassword(os.Stdin.Fd())
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
