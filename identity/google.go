package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
)

func defaultGoogleScopes() []string {
	return []string{"openid", "email", "profile"}
}

type googleProvider struct {
	cfg        Config
	httpClient *http.Client
}

// NewGoogle returns a Provider backed by Google's OpenID Connect flow.
// Config.BaseURL is not meaningful for Google (there is no self-hosted
// Google) and is ignored.
func NewGoogle(cfg Config) Provider {
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = defaultGoogleScopes()
	}
	return &googleProvider{cfg: cfg, httpClient: http.DefaultClient}
}

func (p *googleProvider) Name() string { return "google" }

func (p *googleProvider) AuthURL(state, redirectURL string) string {
	values := url.Values{
		"client_id":     {p.cfg.ClientID},
		"redirect_uri":  {redirectURL},
		"response_type": {"code"},
		"scope":         {strings.Join(p.cfg.Scopes, " ")},
		"state":         {state},
	}
	return googleAuthURL + "?" + values.Encode()
}

func (p *googleProvider) Exchange(ctx context.Context, code, redirectURL string) (Profile, error) {
	token, err := p.exchangeCode(ctx, code, redirectURL)
	if err != nil {
		return Profile{}, err
	}
	// token is used only for the userinfo lookup below and is never
	// returned or otherwise retained past this function.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserInfoURL, nil)
	if err != nil {
		return Profile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return Profile{}, fmt.Errorf("identity: google fetch userinfo: %w", err)
	}
	defer resp.Body.Close()
	payload, err := readLimited(resp.Body)
	if err != nil {
		return Profile{}, fmt.Errorf("identity: google fetch userinfo: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Profile{}, fmt.Errorf("identity: google fetch userinfo: status %d", resp.StatusCode)
	}
	return parseGoogleUserInfo(payload)
}

// googleUserInfo mirrors the fields of Google's OpenID Connect userinfo
// response (https://openidconnect.googleapis.com/v1/userinfo) that this
// package uses. email_verified is documented as a bool but some responses
// carry it as the string "true"/"false"; googleFlexibleBool absorbs both.
type googleUserInfo struct {
	Sub           string             `json:"sub"`
	Email         string             `json:"email"`
	EmailVerified googleFlexibleBool `json:"email_verified"`
	Name          string             `json:"name"`
	Picture       string             `json:"picture"`
}

// googleFlexibleBool unmarshals either a JSON bool or a JSON string
// "true"/"false" into a bool.
type googleFlexibleBool bool

func (b *googleFlexibleBool) UnmarshalJSON(data []byte) error {
	var asBool bool
	if err := json.Unmarshal(data, &asBool); err == nil {
		*b = googleFlexibleBool(asBool)
		return nil
	}
	var asString string
	if err := json.Unmarshal(data, &asString); err != nil {
		return fmt.Errorf("identity: google email_verified: %w", err)
	}
	*b = googleFlexibleBool(asString == "true")
	return nil
}

func parseGoogleUserInfo(payload []byte) (Profile, error) {
	var info googleUserInfo
	if err := json.Unmarshal(payload, &info); err != nil {
		return Profile{}, fmt.Errorf("identity: google userinfo: malformed response")
	}
	if info.Sub == "" {
		return Profile{}, fmt.Errorf("identity: google userinfo: missing sub")
	}
	return Profile{
		Provider:      "google",
		Subject:       info.Sub,
		DisplayName:   info.Name,
		Email:         info.Email,
		EmailVerified: bool(info.EmailVerified),
		AvatarURL:     info.Picture,
	}, nil
}

func (p *googleProvider) exchangeCode(ctx context.Context, code, redirectURL string) (string, error) {
	body := url.Values{
		"client_id":     {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURL},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("identity: google token exchange: %w", err)
	}
	defer resp.Body.Close()
	payload, err := readLimited(resp.Body)
	if err != nil {
		return "", fmt.Errorf("identity: google token exchange: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("identity: google token exchange: status %d", resp.StatusCode)
	}
	var decoded struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", fmt.Errorf("identity: google token exchange: malformed response")
	}
	if decoded.Error != "" {
		return "", fmt.Errorf("identity: google token exchange refused: %s", decoded.Error)
	}
	if decoded.AccessToken == "" {
		return "", fmt.Errorf("identity: google token exchange: empty access token")
	}
	return decoded.AccessToken, nil
}
