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

// maxSetupCodeResponseBytes bounds the setup-code exchange response: a
// credential, an expiry, an org slug, and a server URL — a few hundred bytes
// at most.
const maxSetupCodeResponseBytes = 16 << 10

// SetupCodeResult is the server's answer to exchanging a one-time setup code
// (RFC 0005 Appendix A, "Enrollment through a setup code"). Token is a
// machine credential, identical in shape to a personal access token — never
// log or print it.
type SetupCodeResult struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	// Org is the organization the code was bound to. `rgt connect --setup`
	// carries it into the ordinary connect-once flow exactly where --org
	// would have gone.
	Org string `json:"org"`
	// ServerURL is the server's own idea of its address, distinct from
	// whatever URL the caller used to reach it (a LAN hostname vs. the
	// address the wizard's admin recorded, say). Currently informational: the
	// credential is stored under the URL the caller connected with, matching
	// `rgt auth login`.
	ServerURL string `json:"server_url"`
}

// ExchangeSetupCode calls POST /api/v1/auth/setup-code, trading a one-time
// setup code from the self-hosted onboarding wizard for a machine
// credential. code is bound to a single user and expires 15 minutes after
// issue; machineName identifies this machine in the server's connections
// feed and audit trail — `rgt connect <url> --setup <code>` passes
// os.Hostname().
//
// The two documented failure codes, "setup_code_invalid" and
// "setup_code_expired", come back as a *ServerError so a caller can
// recognise them with IsSetupCodeInvalid / IsSetupCodeExpired and show a
// clear instruction instead of the raw server message.
func ExchangeSetupCode(ctx context.Context, client *http.Client, serverURL, code, machineName string) (SetupCodeResult, error) {
	if client == nil {
		client = http.DefaultClient
	}
	payload, err := json.Marshal(map[string]string{"code": code, "machine_name": machineName})
	if err != nil {
		return SetupCodeResult{}, fmt.Errorf("encode setup-code request: %w", err)
	}
	endpoint := strings.TrimRight(serverURL, "/") + "/api/v1/auth/setup-code"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return SetupCodeResult{}, fmt.Errorf("build setup-code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return SetupCodeResult{}, fmt.Errorf("POST %s: %w", redactURL(endpoint), err)
	}
	defer closeBody(resp)

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSetupCodeResponseBytes+1))
	if err != nil {
		return SetupCodeResult{}, fmt.Errorf("read setup-code response: %w", err)
	}
	if len(data) > maxSetupCodeResponseBytes {
		return SetupCodeResult{}, fmt.Errorf("setup-code response exceeds size limit")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return SetupCodeResult{}, decodeServerError(resp.StatusCode, data)
	}
	var result SetupCodeResult
	if err := json.Unmarshal(data, &result); err != nil {
		return SetupCodeResult{}, fmt.Errorf("decode setup-code response: %w", err)
	}
	if result.Token == "" {
		return SetupCodeResult{}, fmt.Errorf("server returned an empty credential")
	}
	return result, nil
}

// IsSetupCodeInvalid reports whether err is the "setup_code_invalid" error
// POST /api/v1/auth/setup-code returns for a code that was never issued, was
// already used, or does not parse.
func IsSetupCodeInvalid(err error) bool {
	var se *ServerError
	return asServerError(err, &se) && se.Code == "setup_code_invalid"
}

// IsSetupCodeExpired reports whether err is the "setup_code_expired" error
// POST /api/v1/auth/setup-code returns for a code past its 15-minute window.
func IsSetupCodeExpired(err error) bool {
	var se *ServerError
	return asServerError(err, &se) && se.Code == "setup_code_expired"
}
