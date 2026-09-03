package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// defaultGitHubScopes is what NewGitHub requests when Config.Scopes is
// empty: enough to read the profile and verified emails, plus organization
// membership when Config.ReadOrgs is set.
func defaultGitHubScopes(readOrgs bool) []string {
	scopes := []string{"read:user", "user:email"}
	if readOrgs {
		scopes = append(scopes, "read:org")
	}
	return scopes
}

type githubProvider struct {
	cfg          Config
	authorizeURL string
	tokenURL     string
	apiBaseURL   string
	httpClient   *http.Client
}

// NewGitHub returns a Provider backed by GitHub's OAuth apps flow. With
// Config.BaseURL empty it talks to github.com and api.github.com; with
// BaseURL set (a GitHub Enterprise Server instance, e.g.
// "https://github.example.com") it uses "<BaseURL>/login/oauth/..." for the
// authorize and token endpoints and "<BaseURL>/api/v3" for the REST API, per
// GitHub Enterprise Server's documented layout.
func NewGitHub(cfg Config) Provider {
	base := strings.TrimRight(cfg.BaseURL, "/")
	p := &githubProvider{cfg: cfg, httpClient: http.DefaultClient}
	if base == "" {
		p.authorizeURL = "https://github.com/login/oauth/authorize"
		p.tokenURL = "https://github.com/login/oauth/access_token"
		p.apiBaseURL = "https://api.github.com"
	} else {
		p.authorizeURL = base + "/login/oauth/authorize"
		p.tokenURL = base + "/login/oauth/access_token"
		p.apiBaseURL = base + "/api/v3"
	}
	if len(p.cfg.Scopes) == 0 {
		p.cfg.Scopes = defaultGitHubScopes(cfg.ReadOrgs)
	}
	return p
}

func (p *githubProvider) Name() string { return "github" }

func (p *githubProvider) AuthURL(state, redirectURL string) string {
	values := url.Values{
		"client_id":    {p.cfg.ClientID},
		"redirect_uri": {redirectURL},
		"scope":        {strings.Join(p.cfg.Scopes, " ")},
		"state":        {state},
	}
	return p.authorizeURL + "?" + values.Encode()
}

func (p *githubProvider) Exchange(ctx context.Context, code, redirectURL string) (Profile, error) {
	token, err := p.exchangeCode(ctx, code, redirectURL)
	if err != nil {
		return Profile{}, err
	}
	// token is used only for the lookups below and is never returned or
	// otherwise retained past this function.
	user, err := p.fetchUser(ctx, token)
	if err != nil {
		return Profile{}, err
	}
	email, verified, err := p.fetchVerifiedPrimaryEmail(ctx, token)
	if err != nil {
		return Profile{}, err
	}
	var orgs []string
	if p.cfg.ReadOrgs {
		orgs, err = p.fetchOrgs(ctx, token)
		if err != nil {
			return Profile{}, err
		}
	}
	displayName := user.Name
	if displayName == "" {
		displayName = user.Login
	}
	return Profile{
		Provider:      "github",
		Subject:       strconv.FormatInt(user.ID, 10),
		Login:         user.Login,
		DisplayName:   displayName,
		Email:         email,
		EmailVerified: verified,
		Orgs:          orgs,
		AvatarURL:     user.AvatarURL,
	}, nil
}

func (p *githubProvider) exchangeCode(ctx context.Context, code, redirectURL string) (string, error) {
	body := url.Values{
		"client_id":     {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURL},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("identity: github token exchange: %w", err)
	}
	defer resp.Body.Close()
	payload, err := readLimited(resp.Body)
	if err != nil {
		return "", fmt.Errorf("identity: github token exchange: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("identity: github token exchange: status %d", resp.StatusCode)
	}
	var decoded struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", fmt.Errorf("identity: github token exchange: malformed response")
	}
	if decoded.Error != "" {
		return "", fmt.Errorf("identity: github token exchange refused: %s", decoded.Error)
	}
	if decoded.AccessToken == "" {
		return "", fmt.Errorf("identity: github token exchange: empty access token")
	}
	return decoded.AccessToken, nil
}

type githubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

func (p *githubProvider) fetchUser(ctx context.Context, token string) (githubUser, error) {
	var user githubUser
	if err := p.getJSON(ctx, token, p.apiBaseURL+"/user", &user); err != nil {
		return githubUser{}, fmt.Errorf("identity: github fetch user: %w", err)
	}
	return user, nil
}

type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func (p *githubProvider) fetchVerifiedPrimaryEmail(ctx context.Context, token string) (string, bool, error) {
	var emails []githubEmail
	if err := p.getJSON(ctx, token, p.apiBaseURL+"/user/emails", &emails); err != nil {
		return "", false, fmt.Errorf("identity: github fetch emails: %w", err)
	}
	email, verified := chooseVerifiedPrimaryEmail(emails)
	return email, verified, nil
}

// chooseVerifiedPrimaryEmail picks the address GitHub reports as both
// primary and verified. GitHub's own contract is that an account has at
// most one primary email, but a defensive fallback (verified, non-primary)
// keeps sign-in working if that ever isn't true, while never reporting an
// unverified address as verified.
func chooseVerifiedPrimaryEmail(emails []githubEmail) (string, bool) {
	var verifiedFallback string
	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email, true
		}
		if verifiedFallback == "" && e.Verified {
			verifiedFallback = e.Email
		}
	}
	if verifiedFallback != "" {
		return verifiedFallback, true
	}
	return "", false
}

func (p *githubProvider) fetchOrgs(ctx context.Context, token string) ([]string, error) {
	var orgs []struct {
		Login string `json:"login"`
	}
	if err := p.getJSON(ctx, token, p.apiBaseURL+"/user/orgs", &orgs); err != nil {
		return nil, fmt.Errorf("identity: github fetch orgs: %w", err)
	}
	logins := make([]string, 0, len(orgs))
	for _, o := range orgs {
		logins = append(logins, o.Login)
	}
	return logins, nil
}

func (p *githubProvider) getJSON(ctx context.Context, token, target string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "re_gent-identity")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := readLimited(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		return fmt.Errorf("malformed response")
	}
	return nil
}

// readLimited caps a provider response body so a misbehaving or malicious
// endpoint cannot exhaust memory.
func readLimited(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, 1<<20))
}
