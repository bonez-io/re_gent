package selfhosted

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/identity"
)

const (
	oauthNonceCookieName = "__Host-regent_oauth_nonce"
	oauthStateTTL        = 10 * time.Minute
)

// buildIdentityProviders constructs the identity.Handlers mux for whichever
// of GitHub/Google are enabled in the organization's stored auth-method
// settings, decrypting client secrets through box. It is rebuilt at startup
// and every time PUT /api/v1/orgs/{slug}/auth-methods changes the settings,
// so a newly configured provider becomes reachable without a restart. A nil
// return means no organization exists yet (nothing to build against).
func (s *Server) buildIdentityProviders() (http.Handler, error) {
	org, err := s.identities.getOrganization()
	if errors.Is(err, errNoOrganization) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	settings, err := s.identities.getAuthMethodSettings(org.id, org.ServerURL)
	if err != nil {
		return nil, err
	}
	providers := map[string]identity.Provider{}
	if settings.GitHub.Enabled && settings.GitHub.ClientID != "" {
		secret, serr := s.readProviderSecret(org.id, "github")
		if serr != nil {
			return nil, serr
		}
		if secret != "" {
			providers["github"] = identity.NewGitHub(identity.Config{
				ClientID: settings.GitHub.ClientID, ClientSecret: secret,
				BaseURL: settings.GitHub.BaseURL, ReadOrgs: len(org.AllowedGitHubOrgs) > 0,
			})
		}
	}
	if settings.Google.Enabled && settings.Google.ClientID != "" {
		secret, serr := s.readProviderSecret(org.id, "google")
		if serr != nil {
			return nil, serr
		}
		if secret != "" {
			providers["google"] = identity.NewGoogle(identity.Config{ClientID: settings.Google.ClientID, ClientSecret: secret})
		}
	}
	signer := identity.NewHMACSigner(s.oauthStateKey, oauthStateTTL)
	resolver := &identityResolver{store: s.identities, org: org}
	handler := identity.Handlers(providers, signer, resolver,
		identity.WithSessionNonce(func(r *http.Request) string {
			if c, err := r.Cookie(oauthNonceCookieName); err == nil {
				return c.Value
			}
			return ""
		}),
		identity.WithCallbackBaseURL(strings.TrimRight(org.ServerURL, "/")+"/api/v1/auth"),
		identity.WithRateLimit(func(r *http.Request, provider string) bool {
			return s.sessionLimiter.allow("oauth:"+clientKey(r), time.Now())
		}),
	)
	return handler, nil
}

func (s *Server) readProviderSecret(orgID, provider string) (string, error) {
	var enc []byte
	col := "github_client_secret_enc"
	if provider == "google" {
		col = "google_client_secret_enc"
	}
	err := s.identities.db.QueryRow(`SELECT `+col+` FROM auth_method_settings WHERE org_id=?`, orgID).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return s.secrets.open(enc)
}

// refreshIdentityProviders rebuilds the mounted OAuth handler after settings
// change. Failures are logged, not fatal: GitHub/Google sign-in simply stays
// unavailable until the next successful rebuild, while password sign-in
// (the beta's break-glass method) is never affected.
func (s *Server) refreshIdentityProviders() {
	handler, err := s.buildIdentityProviders()
	if err != nil {
		s.logf("rebuild identity providers: %v", err)
		return
	}
	s.identityProvidersMu.Lock()
	s.identityProviders = handler
	s.identityProvidersMu.Unlock()
}

func (s *Server) identityProvidersHandler() http.Handler {
	s.identityProvidersMu.RLock()
	defer s.identityProvidersMu.RUnlock()
	return s.identityProviders
}

func (s *Server) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

// serveIdentityProvider mounts the current OAuth handler for
// "/api/v1/auth/{provider}/{start|callback}", stamping and reading the
// pre-session nonce cookie identity.Handlers binds its signed state to (see
// WithSessionNonce above): "start" mints a fresh nonce if the browser has
// none yet and makes it visible to the nonce accessor within this same
// request; "callback" relies on the cookie the browser already carries from
// the start leg.
func (s *Server) serveIdentityProvider(w http.ResponseWriter, r *http.Request) bool {
	handler := s.identityProvidersHandler()
	if handler == nil {
		return false
	}
	if strings.HasSuffix(r.URL.Path, "/start") {
		if _, err := r.Cookie(oauthNonceCookieName); err != nil {
			nonce, genErr := newSecret("")
			if genErr == nil {
				http.SetCookie(w, &http.Cookie{Name: oauthNonceCookieName, Value: nonce, Path: "/", MaxAge: int(oauthStateTTL.Seconds()),
					HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
				r.AddCookie(&http.Cookie{Name: oauthNonceCookieName, Value: nonce})
			}
		}
	}
	// The identity package's Resolver has no ResponseWriter of its own (it
	// only decides *who* is admitted, never how a session is delivered), so
	// the composition-private responseWriterContextKey below lets
	// identityResolver.Resolve reach back into this response to set the same
	// session cookie every other sign-in path sets, on the one code path
	// (a successful, non-refused Outcome) that needs to.
	r = r.WithContext(withResponseWriter(r.Context(), w))
	handler.ServeHTTP(w, r)
	return true
}

type responseWriterContextKey struct{}

func withResponseWriter(ctx context.Context, w http.ResponseWriter) context.Context {
	return context.WithValue(ctx, responseWriterContextKey{}, w)
}

func responseWriterFromContext(ctx context.Context) (http.ResponseWriter, bool) {
	w, ok := ctx.Value(responseWriterContextKey{}).(http.ResponseWriter)
	return w, ok
}

// identityResolver implements identity.Resolver against the self-hosted
// identity store, applying the RFC 0005 screen 3 admission rules in order:
// invitation match, existing linked identity or matching email, allowed
// GitHub organization, then open join policy. Nothing here is provider
// specific beyond reading Profile.Orgs for the GitHub-organization rule.
type identityResolver struct {
	store *identityStore
	org   Organization
}

func (res *identityResolver) Resolve(ctx context.Context, p identity.Profile, st identity.State) (identity.Outcome, error) {
	if p.Email != "" && !p.EmailVerified {
		// A profile can carry an unverified address; screen 3 only ever
		// admits on a *verified* primary email, so it is never consulted
		// below when this bit is unset.
		p.Email = ""
	}
	outcome, err := res.resolve(ctx, p, st)
	if err != nil || outcome.Refused || outcome.UserID == "" {
		return outcome, err
	}
	// Admitted: deliver the same __Host- session cookie every other sign-in
	// path issues, using the ResponseWriter serveIdentityProvider stashed in
	// ctx (see its comment). The browser then follows outcome.Redirect (or
	// "/") and can read csrf_token off GET /api/v1/auth/me, exactly as the
	// invitation-accept and setup-code flows already expect a caller to.
	if w, ok := responseWriterFromContext(ctx); ok {
		session, _, sessErr := res.store.issueSessionForUser(outcome.UserID)
		if sessErr != nil {
			return identity.Outcome{}, sessErr
		}
		setSessionCookie(w, session, sessionLifetime)
	}
	return outcome, nil
}

func (res *identityResolver) resolve(ctx context.Context, p identity.Profile, st identity.State) (identity.Outcome, error) {
	outcome, matched, err := res.store.resolveByInvitation(res.org, p)
	if err != nil || matched {
		return outcome, err
	}
	outcome, matched, err = res.store.resolveByExistingIdentity(p)
	if err != nil || matched {
		return outcome, err
	}
	if p.Provider == "github" && len(res.org.AllowedGitHubOrgs) > 0 && orgsIntersect(res.org.AllowedGitHubOrgs, p.Orgs) {
		return res.store.provisionFromProfile(res.org, p, "member")
	}
	if res.org.JoinPolicy == "open" {
		return res.store.provisionFromProfile(res.org, p, "member")
	}
	return identity.Outcome{Refused: true, Reason: "not_invited"}, nil
}

func orgsIntersect(allowed, have []string) bool {
	set := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		set[strings.ToLower(a)] = true
	}
	for _, h := range have {
		if set[strings.ToLower(h)] {
			return true
		}
	}
	return false
}

// resolveByInvitation consumes a pending, unexpired invitation whose email or
// username names this profile, creating the account and linking the
// identity. matched is true whenever an invitation existed for this profile
// at all, even one that turned out expired or already used, so the caller
// never falls through to a looser admission rule for someone who was in fact
// invited.
func (s *identityStore) resolveByInvitation(org Organization, p identity.Profile) (identity.Outcome, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return identity.Outcome{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var id, email, username, orgRole, grantsJSON, status, expiresAt string
	row := tx.QueryRow(`SELECT id, email, username, org_role, grants, status, expires_at FROM invitations
WHERE org_id=? AND status='pending' AND ((email<>'' AND email=?) OR (username<>'' AND username=? COLLATE NOCASE))
ORDER BY created_at DESC LIMIT 1`, org.id, p.Email, p.Login)
	err = row.Scan(&id, &email, &username, &orgRole, &grantsJSON, &status, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Outcome{}, false, nil
	}
	if err != nil {
		return identity.Outcome{}, false, err
	}
	expires, _ := parseTime(expiresAt)
	now := s.now()
	if !expires.After(now) {
		return identity.Outcome{Refused: true, Reason: "invitation_expired"}, true, nil
	}
	displayName := p.DisplayName
	loginName := p.Login
	if loginName == "" {
		loginName = strings.SplitN(p.Email, "@", 2)[0]
	}
	loginName, displayName, err = validateUserInput(loginName, orNonEmpty(displayName, loginName))
	if err != nil {
		return identity.Outcome{}, true, err
	}
	userID, err := newID("usr")
	if err != nil {
		return identity.Outcome{}, true, err
	}
	if _, err := tx.Exec(`INSERT INTO users(id, username, display_name, email, org_role, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, loginName, displayName, p.Email, orgRole, formatTime(now)); err != nil {
		return identity.Outcome{}, true, fmt.Errorf("create user from invitation: %w", err)
	}
	if err := linkIdentityTx(tx, userID, p, now); err != nil {
		return identity.Outcome{}, true, err
	}
	if _, err := tx.Exec(`UPDATE invitations SET status='accepted', accepted_at=? WHERE id=?`, formatTime(now), id); err != nil {
		return identity.Outcome{}, true, err
	}
	if err := appendAuditTx(tx, userID, "invitation.accept", "invitation", id, "success", now); err != nil {
		return identity.Outcome{}, true, err
	}
	if err := tx.Commit(); err != nil {
		return identity.Outcome{}, true, err
	}
	return identity.Outcome{UserID: userID}, true, nil
}

// resolveByExistingIdentity admits a profile already linked to a user
// (provider+subject previously seen), or links it now when the profile's
// verified email matches an existing member's.
func (s *identityStore) resolveByExistingIdentity(p identity.Profile) (identity.Outcome, bool, error) {
	var userID string
	err := s.db.QueryRow(`SELECT user_id FROM identities WHERE provider=? AND subject=?`, p.Provider, p.Subject).Scan(&userID)
	if err == nil {
		return identity.Outcome{UserID: userID}, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return identity.Outcome{}, false, err
	}
	if p.Email == "" {
		return identity.Outcome{}, false, nil
	}
	err = s.db.QueryRow(`SELECT id FROM users WHERE email=? COLLATE NOCASE AND disabled_at IS NULL`, p.Email).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Outcome{}, false, nil
	}
	if err != nil {
		return identity.Outcome{}, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return identity.Outcome{}, true, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := linkIdentityTx(tx, userID, p, s.now()); err != nil {
		return identity.Outcome{}, true, err
	}
	if err := tx.Commit(); err != nil {
		return identity.Outcome{}, true, err
	}
	return identity.Outcome{UserID: userID}, true, nil
}

// provisionFromProfile creates a brand-new member account for a profile that
// matched an open-admission rule (allowed GitHub organization, or open join
// policy) rather than an invitation or an existing account.
func (s *identityStore) provisionFromProfile(org Organization, p identity.Profile, orgRole string) (identity.Outcome, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return identity.Outcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	loginName := p.Login
	if loginName == "" {
		loginName = strings.SplitN(p.Email, "@", 2)[0]
	}
	displayName := orNonEmpty(p.DisplayName, loginName)
	loginName, displayName, err = validateUserInput(loginName, displayName)
	if err != nil {
		return identity.Outcome{}, err
	}
	now := s.now()
	userID, err := newID("usr")
	if err != nil {
		return identity.Outcome{}, err
	}
	if _, err := tx.Exec(`INSERT INTO users(id, username, display_name, email, org_role, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, loginName, displayName, p.Email, orgRole, formatTime(now)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			// Username collision from a provider login that happens to match
			// an existing local username: fall back to a suffixed name
			// rather than failing the sign-in outright.
			loginName = loginName + "-" + hex.EncodeToString([]byte(p.Subject))[:6]
			if _, err2 := tx.Exec(`INSERT INTO users(id, username, display_name, email, org_role, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				userID, loginName, displayName, p.Email, orgRole, formatTime(now)); err2 != nil {
				return identity.Outcome{}, err2
			}
		} else {
			return identity.Outcome{}, err
		}
	}
	if err := linkIdentityTx(tx, userID, p, now); err != nil {
		return identity.Outcome{}, err
	}
	if err := appendAuditTx(tx, userID, "identity.provision", "user", userID, "success", now); err != nil {
		return identity.Outcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return identity.Outcome{}, err
	}
	return identity.Outcome{UserID: userID}, nil
}

func linkIdentityTx(tx *sql.Tx, userID string, p identity.Profile, now time.Time) error {
	_, err := tx.Exec(`INSERT INTO identities(user_id, provider, subject, login, email, created_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(provider, subject) DO UPDATE SET login=excluded.login, email=excluded.email`,
		userID, p.Provider, p.Subject, p.Login, p.Email, formatTime(now))
	return err
}

func orNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// issueSessionForUser mints the session cookie value and CSRF token for a
// user id already admitted by the resolver, exactly as any other sign-in
// path does. Used by the callback route after identity.Handlers' Resolver
// step has approved the profile.
func (s *identityStore) issueSessionForUser(userID string) (string, string, error) {
	return s.createSession(userID)
}
