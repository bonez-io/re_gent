package selfhosted

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestOnboardingRoutesDenyAnonymous exercises every route this stream added
// for RFC 0005, asserting each denies an anonymous caller with 401 except the
// ones Appendix A lists as public: login, setup-code exchange, the two
// invitation routes, and the GitHub/Google start/callback routes (which 404
// with no provider configured, never 401, since the route family itself is
// public — see isProviderRoute).
func TestOnboardingRoutesDenyAnonymous(t *testing.T) {
	srv, _, err := New(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	authenticated := []struct {
		method, path string
	}{
		{http.MethodPost, "/api/v1/auth/password"},
		{http.MethodPost, "/api/v1/auth/logout"},
		{http.MethodPost, "/api/v1/orgs"},
		{http.MethodGet, "/api/v1/orgs/some-org"},
		{http.MethodPatch, "/api/v1/orgs/some-org"},
		{http.MethodPost, "/api/v1/orgs/some-org/onboarding"},
		{http.MethodGet, "/api/v1/orgs/some-org/auth-methods"},
		{http.MethodPut, "/api/v1/orgs/some-org/auth-methods"},
		{http.MethodPost, "/api/v1/orgs/some-org/setup-codes"},
		{http.MethodGet, "/api/v1/orgs/some-org/connections"},
		{http.MethodGet, "/api/v1/orgs/some-org/invitations"},
		{http.MethodPost, "/api/v1/orgs/some-org/invitations"},
		{http.MethodDelete, "/api/v1/orgs/some-org/invitations/inv_x"},
		{http.MethodGet, "/api/v1/orgs/some-org/members"},
		{http.MethodPatch, "/api/v1/orgs/some-org/members/usr_x"},
		{http.MethodPost, "/api/v1/admin/backup"},
	}
	for _, tc := range authenticated {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			resp := serveRequest(srv, tc.method, tc.path, "", "", nil)
			assertStatus(t, resp, http.StatusUnauthorized)
			if resp.Header().Get("WWW-Authenticate") == "" {
				t.Errorf("%s %s: 401 response omitted WWW-Authenticate", tc.method, tc.path)
			}
		})
	}

	public := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "nobody", "password": "wrong-password"}},
		{http.MethodPost, "/api/v1/auth/setup-code", map[string]string{"code": "0000-0000", "machine_name": "anon"}},
		{http.MethodGet, "/api/v1/invitations/not-a-real-token", nil},
		{http.MethodPost, "/api/v1/invitations/not-a-real-token/accept", map[string]string{"display_name": "x", "password": "whatever-12345"}},
	}
	for _, tc := range public {
		t.Run(tc.method+" "+tc.path+" (public)", func(t *testing.T) {
			resp := serveRequest(srv, tc.method, tc.path, "", "", tc.body)
			// A public route may still answer 401 for a bad credential
			// (login) or 400/404/410 for a bad token/invitation — what must
			// never happen is the anonymous-denial path, which is the one
			// that stamps WWW-Authenticate (see writeAccessError).
			if resp.Header().Get("WWW-Authenticate") != "" {
				t.Errorf("%s %s is documented public in RFC 0005 Appendix A but was denied as anonymous (WWW-Authenticate present): status=%d body=%s",
					tc.method, tc.path, resp.Code, resp.Body.String())
			}
		})
	}
}

// TestSetupCodeReuseAndExpiryFail covers two of RFC 0005's acceptance
// criteria directly: "Reusing a setup code fails; an expired one fails with a
// clear message."
func TestSetupCodeReuseAndExpiryFail(t *testing.T) {
	srv, setup, err := New(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	cookie, csrf, slug := onboardAdmin(t, srv, setup, "Setup Code Org")

	created := serveRequestHeaders(srv, http.MethodPost, "/api/v1/orgs/"+slug+"/setup-codes", "", cookie, map[string]any{}, map[string]string{csrfHeaderName: csrf})
	assertStatus(t, created, http.StatusCreated)
	var codeResp struct {
		Code string `json:"code"`
	}
	decodeResponse(t, created, &codeResp)
	if codeResp.Code == "" {
		t.Fatalf("setup code response missing code: %s", created.Body.String())
	}

	first := serveRequest(srv, http.MethodPost, "/api/v1/auth/setup-code", "", "", map[string]string{"code": codeResp.Code, "machine_name": "laptop"})
	assertStatus(t, first, http.StatusCreated)

	reused := serveRequest(srv, http.MethodPost, "/api/v1/auth/setup-code", "", "", map[string]string{"code": codeResp.Code, "machine_name": "laptop-2"})
	assertStatus(t, reused, http.StatusBadRequest)
	var reusedBody struct {
		Code string `json:"code"`
	}
	decodeResponse(t, reused, &reusedBody)
	if reusedBody.Code != "setup_code_invalid" {
		t.Fatalf("reused setup code response code = %q, want setup_code_invalid: %s", reusedBody.Code, reused.Body.String())
	}

	// A freshly minted code that has aged past its 15-minute expiry fails
	// with setup_code_expired rather than setup_code_invalid.
	expiring := serveRequestHeaders(srv, http.MethodPost, "/api/v1/orgs/"+slug+"/setup-codes", "", cookie, map[string]any{}, map[string]string{csrfHeaderName: csrf})
	assertStatus(t, expiring, http.StatusCreated)
	var expiringResp struct {
		Code string `json:"code"`
	}
	decodeResponse(t, expiring, &expiringResp)
	realNow := srv.identities.now
	srv.identities.now = func() time.Time { return realNow().Add(16 * time.Minute) }
	expired := serveRequest(srv, http.MethodPost, "/api/v1/auth/setup-code", "", "", map[string]string{"code": expiringResp.Code, "machine_name": "laptop-3"})
	assertStatus(t, expired, http.StatusBadRequest)
	var expiredBody struct {
		Code string `json:"code"`
	}
	decodeResponse(t, expired, &expiredBody)
	if expiredBody.Code != "setup_code_expired" {
		t.Fatalf("expired setup code response code = %q, want setup_code_expired: %s", expiredBody.Code, expired.Body.String())
	}
}

// TestInvitationAcceptCreatesMember covers the acceptance criterion "An
// invitee reaches a signed-in state from the link alone."
func TestInvitationAcceptCreatesMember(t *testing.T) {
	srv, setup, err := New(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	cookie, csrf, slug := onboardAdmin(t, srv, setup, "Invitation Org")

	invite := serveRequestHeaders(srv, http.MethodPost, "/api/v1/orgs/"+slug+"/invitations", "", cookie,
		map[string]any{"username": "teammate", "org_role": "member", "grants": []map[string]string{}},
		map[string]string{csrfHeaderName: csrf})
	assertStatus(t, invite, http.StatusCreated)
	var inviteResp struct {
		Link string `json:"link"`
	}
	decodeResponse(t, invite, &inviteResp)
	if inviteResp.Link == "" {
		t.Fatalf("invitation response missing link: %s", invite.Body.String())
	}
	token := inviteResp.Link[strings.LastIndex(inviteResp.Link, "/")+1:]

	public := serveRequest(srv, http.MethodGet, "/api/v1/invitations/"+token, "", "", nil)
	assertStatus(t, public, http.StatusOK)

	accept := serveRequest(srv, http.MethodPost, "/api/v1/invitations/"+token+"/accept", "", "",
		map[string]string{"display_name": "Teammate", "password": "a-strong-enough-password"})
	assertStatus(t, accept, http.StatusCreated)
	var acceptResp struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeResponse(t, accept, &acceptResp)
	if acceptResp.User.ID == "" {
		t.Fatalf("accept response missing user id: %s", accept.Body.String())
	}

	members, err := srv.identities.listOrgMembers()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range members {
		if m.UserID == acceptResp.User.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("accepted invitee %q is not among org members: %#v", acceptResp.User.ID, members)
	}

	// The token is single-use: accepting it again fails.
	again := serveRequest(srv, http.MethodPost, "/api/v1/invitations/"+token+"/accept", "", "",
		map[string]string{"display_name": "Teammate Again", "password": "another-strong-password"})
	if again.Code == http.StatusCreated {
		t.Fatal("accepting an already-accepted invitation succeeded a second time")
	}
}

// TestLastAdminCannotBeDemoted covers the last_admin guard on
// PATCH /api/v1/orgs/{slug}/members/{user_id}.
func TestLastAdminCannotBeDemoted(t *testing.T) {
	srv, setup, err := New(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	cookie, csrf, slug := onboardAdmin(t, srv, setup, "Last Admin Org")

	me := serveRequest(srv, http.MethodGet, "/api/v1/auth/me", "", cookie, nil)
	assertStatus(t, me, http.StatusOK)
	var meResp struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeResponse(t, me, &meResp)

	demote := serveRequestHeaders(srv, http.MethodPatch, "/api/v1/orgs/"+slug+"/members/"+meResp.User.ID, "", cookie,
		map[string]string{"role": "member"}, map[string]string{csrfHeaderName: csrf})
	assertStatus(t, demote, http.StatusBadRequest)
	var demoteBody struct {
		Code string `json:"code"`
	}
	decodeResponse(t, demote, &demoteBody)
	if demoteBody.Code != "last_admin" {
		t.Fatalf("demoting the last admin response code = %q, want last_admin: %s", demoteBody.Code, demote.Body.String())
	}

	// Promoting a second member to admin, then demoting the first, succeeds.
	invite := serveRequestHeaders(srv, http.MethodPost, "/api/v1/orgs/"+slug+"/invitations", "", cookie,
		map[string]any{"username": "second-admin", "org_role": "admin", "grants": []map[string]string{}},
		map[string]string{csrfHeaderName: csrf})
	assertStatus(t, invite, http.StatusCreated)
	var inviteResp struct {
		Link string `json:"link"`
	}
	decodeResponse(t, invite, &inviteResp)
	token := inviteResp.Link[strings.LastIndex(inviteResp.Link, "/")+1:]
	accept := serveRequest(srv, http.MethodPost, "/api/v1/invitations/"+token+"/accept", "", "",
		map[string]string{"display_name": "Second Admin", "password": "yet-another-strong-password"})
	assertStatus(t, accept, http.StatusCreated)

	nowDemotable := serveRequestHeaders(srv, http.MethodPatch, "/api/v1/orgs/"+slug+"/members/"+meResp.User.ID, "", cookie,
		map[string]string{"role": "member"}, map[string]string{csrfHeaderName: csrf})
	assertStatus(t, nowDemotable, http.StatusNoContent)
}

// TestOrgScopedProjectsRouteNamesTheSingleRegistry covers the path the CLI
// takes after a setup-code exchange: it enrolls through
// /api/v1/orgs/{slug}/projects (RFC 0005 Appendix A), which on self-hosted
// must be the same registry as /api/v1/projects rather than a namespace per
// slug, and any other slug must be concealed as 404.
func TestOrgScopedProjectsRouteNamesTheSingleRegistry(t *testing.T) {
	srv, setup, err := New(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	cookie, csrf, slug := onboardAdmin(t, srv, setup, "Scoped Org")

	created := serveRequestHeaders(srv, http.MethodPost, "/api/v1/orgs/"+slug+"/setup-codes", "", cookie, map[string]any{}, map[string]string{csrfHeaderName: csrf})
	assertStatus(t, created, http.StatusCreated)
	var codeResp struct {
		Code string `json:"code"`
	}
	decodeResponse(t, created, &codeResp)
	exchanged := serveRequest(srv, http.MethodPost, "/api/v1/auth/setup-code", "", "", map[string]string{"code": codeResp.Code, "machine_name": "laptop"})
	assertStatus(t, exchanged, http.StatusCreated)
	var cred struct {
		Token string `json:"token"`
		Org   string `json:"org"`
	}
	decodeResponse(t, exchanged, &cred)
	if cred.Org != slug {
		t.Fatalf("setup-code org = %q, want %q", cred.Org, slug)
	}
	bearer := "Bearer " + cred.Token
	body := map[string]any{"fingerprint": strings.Repeat("ab", 32), "remote": "github.com/acme/arm", "root_commit": strings.Repeat("c", 40), "display_name": "arm"}

	first := serveRequest(srv, http.MethodPost, "/api/v1/orgs/"+slug+"/projects", bearer, "", body)
	assertStatus(t, first, http.StatusCreated)
	var firstResp struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		ID string `json:"id"`
	}
	decodeResponse(t, first, &firstResp)
	id := firstResp.Project.ID
	if id == "" {
		id = firstResp.ID
	}
	if id == "" {
		t.Fatalf("scoped enrollment returned no project id: %s", first.Body.String())
	}

	again := serveRequest(srv, http.MethodPost, "/api/v1/projects", bearer, "", body)
	assertStatus(t, again, http.StatusOK)
	if !strings.Contains(again.Body.String(), id) {
		t.Fatalf("unscoped re-enrollment did not land on %s: %s", id, again.Body.String())
	}

	listed := serveRequest(srv, http.MethodGet, "/api/v1/orgs/"+slug+"/projects", bearer, "", nil)
	assertStatus(t, listed, http.StatusOK)
	if !strings.Contains(listed.Body.String(), id) {
		t.Fatalf("scoped list omitted %s: %s", id, listed.Body.String())
	}

	other := serveRequest(srv, http.MethodPost, "/api/v1/orgs/not-this-org/projects", bearer, "", body)
	assertStatus(t, other, http.StatusNotFound)
}
