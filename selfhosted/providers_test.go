package selfhosted

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/identity"
)

// fakeProviderServer builds a Server whose mounted identity.Handlers uses
// identity.NewFake instead of real GitHub/Google, so the OAuth start/
// callback/Resolver/session-cookie wiring in providers.go — the part of this
// stream's brief that changed after S5's identity package landed — can be
// verified without a real OAuth app. It bypasses buildIdentityProviders
// (which only ever constructs identity.NewGitHub/NewGoogle from stored
// settings, deliberately: nothing production-reachable should be able to
// select the fake provider) and instead sets srv.identityProviders directly,
// exactly as refreshIdentityProviders would after a real build.
func fakeProviderServer(t *testing.T, profiles map[string]identity.Profile) (*Server, Organization, string, string) {
	t.Helper()
	srv, setup, err := New(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	cookie, csrf, slug := onboardAdmin(t, srv, setup, "Fake Provider Org")
	org, err := srv.identities.getOrganizationBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	remountFake(srv, org, profiles)
	return srv, org, cookie, csrf
}

// remountFake (re-)mounts an identity.Handlers backed by identity.NewFake and
// a fresh identityResolver closed over org, exactly as
// buildIdentityProviders/refreshIdentityProviders would after a real
// GitHub/Google settings change — used by tests that need the Resolver to
// see updated Organization state (e.g. a changed join_policy) without
// depending on buildIdentityProviders' real-provider-only construction.
func remountFake(srv *Server, org Organization, profiles map[string]identity.Profile) {
	providers := map[string]identity.Provider{"fake": identity.NewFake(profiles)}
	signer := identity.NewHMACSigner(srv.oauthStateKey, 10*time.Minute)
	resolver := &identityResolver{store: srv.identities, org: org}
	handler := identity.Handlers(providers, signer, resolver,
		identity.WithSessionNonce(func(r *http.Request) string {
			if c, err := r.Cookie(oauthNonceCookieName); err == nil {
				return c.Value
			}
			return ""
		}),
		identity.WithCallbackBaseURL("http://example.invalid/api/v1/auth"),
	)
	srv.identityProvidersMu.Lock()
	srv.identityProviders = handler
	srv.identityProvidersMu.Unlock()
}

// adminUserID reads back the onboarded admin's user id through the session
// cookie onboardAdmin returned, for store calls (like updateOrganization)
// that take an actor id directly rather than an authenticated request.
func adminUserID(t *testing.T, srv *Server, cookie string) string {
	t.Helper()
	resp := serveRequest(srv, http.MethodGet, "/api/v1/auth/me", "", cookie, nil)
	assertStatus(t, resp, http.StatusOK)
	var body struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeResponse(t, resp, &body)
	return body.User.ID
}

func strPtr(s string) *string { return &s }

// startFake drives GET /api/v1/auth/fake/start and returns the signed state
// token plus the nonce cookie the browser would carry into the callback.
func startFake(t *testing.T, srv *Server) (state string, nonceCookie *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/fake/start", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("start status = %d, want %d; body=%s", rec.Code, http.StatusFound, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc == "" {
		t.Fatal("start response missing Location")
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse start redirect: %v", err)
	}
	state = u.Query().Get("state")
	if state == "" {
		t.Fatalf("start redirect missing state: %s", loc)
	}
	cookies := rec.Result().Cookies()
	for _, c := range cookies {
		if c.Name == oauthNonceCookieName {
			nonceCookie = c
		}
	}
	if nonceCookie == nil {
		t.Fatal("start response did not set the OAuth nonce cookie")
	}
	return state, nonceCookie
}

func callbackFake(t *testing.T, srv *Server, code, state string, nonceCookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/fake/callback?code="+code+"&state="+url.QueryEscape(state), nil)
	req.AddCookie(nonceCookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestOAuthOpenJoinPolicyAdmitsAndIssuesSession covers the "open join
// policy" branch of the RFC 0005 screen 3 admission rules end to end: a
// profile with no invitation and no allowed-org match is still admitted
// because the organization's join_policy is "open", and the callback leaves
// the browser with a working __Host- session cookie.
func TestOAuthOpenJoinPolicyAdmitsAndIssuesSession(t *testing.T) {
	profile := identity.Profile{Login: "newperson", DisplayName: "New Person", Email: "newperson@example.com", EmailVerified: true}
	srv, org, adminCookie, adminCSRF := fakeProviderServer(t, map[string]identity.Profile{"code1": profile})

	// Changing join_policy must be visible to the next-built Resolver: the
	// mounted OAuth handler's Resolver closes over an Organization snapshot,
	// which is exactly why handleOrgItem's real PATCH handler calls
	// refreshIdentityProviders() after every update. This test updates the
	// org directly and re-mounts the fake handler itself so it can assert on
	// the Resolver's behavior against fresh org state without depending on
	// buildIdentityProviders' real-provider-only construction (which has no
	// stored GitHub/Google settings to build from here).
	updatedOrg, err := srv.identities.updateOrganization(adminUserID(t, srv, adminCookie), org.Slug, orgPatch{JoinPolicy: strPtr("open")})
	if err != nil {
		t.Fatal(err)
	}
	_ = adminCSRF
	remountFake(srv, updatedOrg, map[string]identity.Profile{"code1": profile})

	state, nonce := startFake(t, srv)
	rec := callbackFake(t, srv, "code1", state, nonce)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want %d; body=%s", rec.Code, http.StatusFound, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc == "/not-invited" || (len(loc) >= 12 && loc[:12] == "/not-invited") {
		t.Fatalf("open join policy profile was refused: redirected to %s", loc)
	}
	var sessionCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatalf("callback did not set a session cookie: %#v", rec.Result().Cookies())
	}

	me := serveRequest(srv, http.MethodGet, "/api/v1/auth/me", "", sessionCookie.Name+"="+sessionCookie.Value, nil)
	assertStatus(t, me, http.StatusOK)
	var meResp struct {
		User struct {
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"user"`
	}
	decodeResponse(t, me, &meResp)
	if meResp.User.Username != "newperson" || meResp.User.Email != "newperson@example.com" {
		t.Fatalf("unexpected admitted user: %#v", meResp.User)
	}
}

// TestOAuthRefusesUninvitedWhenClosed covers the opposite: invite_only (the
// default) with no invitation, no linked identity, and no allowed-org match
// must refuse, per RFC 0005's acceptance criterion "an uninvited GitHub user
// is refused with the not-invited page and no account created."
func TestOAuthRefusesUninvitedWhenClosed(t *testing.T) {
	profile := identity.Profile{Login: "stranger", DisplayName: "Stranger", Email: "stranger@example.com", EmailVerified: true}
	srv, _, _, _ := fakeProviderServer(t, map[string]identity.Profile{"code1": profile})

	before, err := srv.identities.listOrgMembers()
	if err != nil {
		t.Fatal(err)
	}

	state, nonce := startFake(t, srv)
	rec := callbackFake(t, srv, "code1", state, nonce)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want %d", rec.Code, http.StatusFound)
	}
	loc := rec.Header().Get("Location")
	if len(loc) < 12 || loc[:12] != "/not-invited" {
		t.Fatalf("uninvited profile was not refused: redirected to %s", loc)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			t.Fatal("a session cookie was issued for a refused sign-in")
		}
	}

	after, err := srv.identities.listOrgMembers()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("a refused sign-in created an account: before=%d after=%d", len(before), len(after))
	}
}

// TestOAuthInvitationMatchAdmits covers screen 3 rule 1: an invitation naming
// the profile's login or verified email is consumed and the account created.
func TestOAuthInvitationMatchAdmits(t *testing.T) {
	profile := identity.Profile{Login: "invited-login", DisplayName: "Invited Person", Email: "invited@example.com", EmailVerified: true}
	srv, org, adminCookie, adminCSRF := fakeProviderServer(t, map[string]identity.Profile{"code1": profile})

	_, _, err := srv.identities.createInvitation(org.id, "test-actor", invitationCreate{Username: "invited-login", OrgRole: "member", Grants: nil})
	if err != nil {
		t.Fatal(err)
	}
	_ = adminCookie
	_ = adminCSRF

	state, nonce := startFake(t, srv)
	rec := callbackFake(t, srv, "code1", state, nonce)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want %d; body=%s", rec.Code, http.StatusFound, rec.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatalf("invited profile was not admitted: redirected to %s", rec.Header().Get("Location"))
	}
	invitations, err := srv.identities.listInvitations(org.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(invitations) != 1 || invitations[0].Status != "accepted" {
		t.Fatalf("invitation was not marked accepted: %#v", invitations)
	}
}
