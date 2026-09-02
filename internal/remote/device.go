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

// maxAuthResponseBytes bounds every response in this file: a token pair, a
// device authorization, or a pending-state error, all of which are a handful
// of short fields.
const maxAuthResponseBytes = 16 << 10

// DeviceAuthorization is the server's answer to starting a device login
// (RFC 0004, "CLI flow"): a code to show the user and a code the CLI polls
// with.
type DeviceAuthorization struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

// TokenPair is an access/refresh token pair, returned by completing a device
// login or by refreshing one.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// DevicePollError reports one of the token endpoint's pending states, which
// are not failures so much as "ask again": "authorization_pending" (the user
// has not approved yet), "slow_down" (poll less often), "expired" (the device
// code's window closed), or "denied" (the user declined).
type DevicePollError struct {
	Code string
}

func (e *DevicePollError) Error() string {
	return "device login: " + e.Code
}

// StartDeviceAuthorization begins a device login: POST /api/v1/auth/device.
func StartDeviceAuthorization(ctx context.Context, client *http.Client, serverURL string) (DeviceAuthorization, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+"/api/v1/auth/device", nil)
	if err != nil {
		return DeviceAuthorization{}, fmt.Errorf("build device authorization request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return DeviceAuthorization{}, fmt.Errorf("start device login with %s: %w", serverURL, err)
	}
	defer closeBody(resp)

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAuthResponseBytes+1))
	if err != nil {
		return DeviceAuthorization{}, fmt.Errorf("read device authorization response: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return DeviceAuthorization{}, decodeServerError(resp.StatusCode, data)
	}
	var auth DeviceAuthorization
	if err := json.Unmarshal(data, &auth); err != nil {
		return DeviceAuthorization{}, fmt.Errorf("decode device authorization response: %w", err)
	}
	if auth.DeviceCode == "" || auth.UserCode == "" {
		return DeviceAuthorization{}, fmt.Errorf("server omitted the device or user code")
	}
	return auth, nil
}

// PollDeviceToken checks whether a device login has been approved:
// POST /api/v1/auth/device/token. A pending, throttled, expired, or denied
// state is reported as a *DevicePollError rather than folded into a generic
// error, so a caller's polling loop can switch on it without string matching.
func PollDeviceToken(ctx context.Context, client *http.Client, serverURL, deviceCode string) (TokenPair, error) {
	if client == nil {
		client = http.DefaultClient
	}
	payload, err := json.Marshal(map[string]string{"device_code": deviceCode})
	if err != nil {
		return TokenPair{}, fmt.Errorf("encode device poll request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+"/api/v1/auth/device/token", bytes.NewReader(payload))
	if err != nil {
		return TokenPair{}, fmt.Errorf("build device poll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return TokenPair{}, fmt.Errorf("poll device login with %s: %w", serverURL, err)
	}
	defer closeBody(resp)

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAuthResponseBytes+1))
	if err != nil {
		return TokenPair{}, fmt.Errorf("read device poll response: %w", err)
	}

	if resp.StatusCode == http.StatusOK {
		var pair TokenPair
		if err := json.Unmarshal(data, &pair); err != nil {
			return TokenPair{}, fmt.Errorf("decode device poll response: %w", err)
		}
		if pair.AccessToken == "" {
			return TokenPair{}, fmt.Errorf("server returned an empty access token")
		}
		return pair, nil
	}

	var body struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(data, &body) == nil && body.Code != "" {
		return TokenPair{}, &DevicePollError{Code: body.Code}
	}
	return TokenPair{}, decodeServerError(resp.StatusCode, data)
}

// RefreshTokens exchanges a refresh token for a new access/refresh pair:
// POST /api/v1/auth/token/refresh. It is stateless — it neither reads nor
// writes any client or config state — so it is equally usable from the
// HTTPClient's automatic 401 recovery and from an explicit `rgt auth`
// command.
func RefreshTokens(ctx context.Context, client *http.Client, serverURL, refreshToken string) (TokenPair, error) {
	if client == nil {
		client = http.DefaultClient
	}
	payload, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return TokenPair{}, fmt.Errorf("encode refresh request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+"/api/v1/auth/token/refresh", bytes.NewReader(payload))
	if err != nil {
		return TokenPair{}, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return TokenPair{}, fmt.Errorf("refresh token with %s: %w", serverURL, err)
	}
	defer closeBody(resp)

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAuthResponseBytes+1))
	if err != nil {
		return TokenPair{}, fmt.Errorf("read refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return TokenPair{}, decodeServerError(resp.StatusCode, data)
	}
	var pair TokenPair
	if err := json.Unmarshal(data, &pair); err != nil {
		return TokenPair{}, fmt.Errorf("decode refresh response: %w", err)
	}
	if pair.AccessToken == "" {
		return TokenPair{}, fmt.Errorf("server returned an empty access token")
	}
	return pair, nil
}
