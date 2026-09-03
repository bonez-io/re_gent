package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxPasswordResponseBytes bounds a login or token-creation response — a
// user object, a CSRF token, and a credential secret, none of which are ever
// large.
const maxPasswordResponseBytes = 16 << 10

// csrfHeaderName mirrors selfhosted/server.go's csrfHeaderName exactly: the
// header a session-cookie-authenticated mutation must carry (RFC 0003's CSRF
// rule, reused unchanged by RFC 0005's "Sessions, the __Host- cookie, and
// the CSRF header are exactly RFC 0003's").
const csrfHeaderName = "X-Regent-CSRF"

// PasswordUser is the signed-in identity POST /api/v1/auth/login returns
// (RFC 0005 Appendix A, "Sign-in").
type PasswordUser struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

// PasswordLoginResult is the server's answer to POST /api/v1/auth/login.
type PasswordLoginResult struct {
	User PasswordUser `json:"user"`
	// CSRF must accompany every session-cookie-authenticated mutation in the
	// header named by csrfHeaderName — here, the machine-credential creation
	// CreateMachineCredential performs next.
	CSRF string `json:"csrf"`
	// PasswordChangeRequired is true while the self-hosted instance's
	// initial admin password is still in force (RFC 0005, "Step 1: sign in
	// and the wizard"). A caller must not proceed to mint a machine
	// credential while this is true — the admin has not finished onboarding.
	PasswordChangeRequired bool `json:"password_change_required"`
}

// PasswordLogin signs in with a username and password against a self-hosted
// server's password auth method (RFC 0005, capabilities auth_methods
// contains "password"): POST /api/v1/auth/login, public.
//
// client must carry a non-nil Jar: the server's session cookie ("+ cookie"
// in Appendix A) is captured there, and CreateMachineCredential depends on
// the same client presenting it on the next request — exactly how a browser
// would behave, and exactly what lets this package avoid knowing the
// cookie's name.
func PasswordLogin(ctx context.Context, client *http.Client, serverURL, username, password string) (PasswordLoginResult, error) {
	if client == nil || client.Jar == nil {
		return PasswordLoginResult{}, fmt.Errorf("password login requires an http.Client with a cookie jar")
	}
	payload, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return PasswordLoginResult{}, fmt.Errorf("encode login request: %w", err)
	}
	endpoint := strings.TrimRight(serverURL, "/") + "/api/v1/auth/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return PasswordLoginResult{}, fmt.Errorf("build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return PasswordLoginResult{}, fmt.Errorf("POST %s: %w", redactURL(endpoint), err)
	}
	defer closeBody(resp)

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPasswordResponseBytes+1))
	if err != nil {
		return PasswordLoginResult{}, fmt.Errorf("read login response: %w", err)
	}
	if len(data) > maxPasswordResponseBytes {
		return PasswordLoginResult{}, fmt.Errorf("login response exceeds size limit")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return PasswordLoginResult{}, decodeServerError(resp.StatusCode, data)
	}
	var result PasswordLoginResult
	if err := json.Unmarshal(data, &result); err != nil {
		return PasswordLoginResult{}, fmt.Errorf("decode login response: %w", err)
	}
	if result.User.ID == "" || result.User.Username == "" {
		return PasswordLoginResult{}, fmt.Errorf("server omitted the signed-in user")
	}
	return result, nil
}

// CreateMachineCredential calls the pre-existing personal-access-token route
// (selfhosted/server.go, POST /api/v1/auth/tokens) authenticated by the
// session cookie carried in client's jar plus the CSRF header the login
// response returned — exactly what a browser's own "create a token" action
// does. name becomes the token's display name in the server's token list,
// e.g. "<hostname> (cli)".
//
// This is deliberately not part of the RFC 0005 Appendix A route table: it
// reuses machinery that already existed before this RFC rather than adding a
// new one, which is what "through the existing PAT creation route" means.
func CreateMachineCredential(ctx context.Context, client *http.Client, serverURL, csrfToken, name string) (string, error) {
	if client == nil || client.Jar == nil {
		return "", fmt.Errorf("create machine credential requires an http.Client with a cookie jar")
	}
	payload, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return "", fmt.Errorf("encode token request: %w", err)
	}
	endpoint := strings.TrimRight(serverURL, "/") + "/api/v1/auth/tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(csrfHeaderName, csrfToken)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("POST %s: %w", redactURL(endpoint), err)
	}
	defer closeBody(resp)

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPasswordResponseBytes+1))
	if err != nil {
		return "", fmt.Errorf("read token response: %w", err)
	}
	if len(data) > maxPasswordResponseBytes {
		return "", fmt.Errorf("token response exceeds size limit")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", decodeServerError(resp.StatusCode, data)
	}
	var result struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if result.Secret == "" {
		return "", fmt.Errorf("server returned an empty credential")
	}
	return result.Secret, nil
}
