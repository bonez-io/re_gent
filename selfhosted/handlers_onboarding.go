package selfhosted

import (
	"archive/tar"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/server"
	"github.com/bonez-io/re_gent/serverauth"
)

// ---- login / password / logout ------------------------------------------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !s.loginIPLimiter.allow(clientKey(r), time.Now()) {
		w.Header().Set("Retry-After", "60")
		writeCodedError(w, http.StatusTooManyRequests, "too many login attempts", "rate_limited")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	if !s.loginUserLimiter.allow("user:"+strings.ToLower(req.Username), time.Now()) {
		w.Header().Set("Retry-After", "60")
		writeCodedError(w, http.StatusTooManyRequests, "too many login attempts", "rate_limited")
		return
	}
	user, err := s.identities.verifyLogin(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, serverauth.ErrUnauthenticated) {
			writeCodedError(w, http.StatusUnauthorized, "invalid username or password", "invalid_credentials")
			return
		}
		writeCodedError(w, http.StatusInternalServerError, "login failed", "internal")
		return
	}
	session, csrf, err := s.identities.createSession(user.ID)
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "login failed", "internal")
		return
	}
	changeRequired, err := s.identities.userPasswordChangeRequired(user.ID)
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "login failed", "internal")
		return
	}
	setSessionCookie(w, session, sessionLifetime)
	writeJSON(w, http.StatusCreated, map[string]any{
		"user": user, "csrf": csrf, "password_change_required": changeRequired,
	})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	auth, err := s.require(w, r)
	if err != nil {
		return
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	if err := s.identities.changePassword(auth.user.ID, req.Current, req.New, false); err != nil {
		switch {
		case errors.Is(err, errPasswordTooShort):
			writeCodedError(w, http.StatusBadRequest, "password must be at least 12 characters", "password_too_short")
		case errors.Is(err, serverauth.ErrUnauthenticated):
			writeCodedError(w, http.StatusUnauthorized, "current password is incorrect", "invalid_credentials")
		default:
			writeCodedError(w, http.StatusInternalServerError, "change password failed", "internal")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	auth, err := s.require(w, r)
	if err != nil {
		return
	}
	if auth.tokenKind == "session" {
		if err := s.identities.revokeSession(auth); err != nil {
			writeCodedError(w, http.StatusInternalServerError, "logout failed", "internal")
			return
		}
		clearSessionCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- onboarding -----------------------------------------------------

func (s *Server) handleOnboardingAdmin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var req struct {
		Org struct {
			DisplayName string `json:"display_name"`
			Slug        string `json:"slug"`
			ServerURL   string `json:"server_url"`
			JoinPolicy  string `json:"join_policy"`
			DefaultRole string `json:"default_role"`
		} `json:"org"`
		Admin struct {
			Username        string `json:"username"`
			DisplayName     string `json:"display_name"`
			Email           string `json:"email"`
			NewPassword     string `json:"new_password"`
			CurrentPassword string `json:"current_password"`
		} `json:"admin"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	if req.Org.DisplayName == "" {
		writeCodedError(w, http.StatusBadRequest, "org.display_name is required", "invalid_request")
		return
	}

	storeReq := onboardingAdminRequest{
		NewUsername: req.Admin.Username, DisplayName: req.Admin.DisplayName, Email: req.Admin.Email,
		NewPassword: req.Admin.NewPassword,
		Org: orgCreate{DisplayName: req.Org.DisplayName, Slug: req.Org.Slug, ServerURL: req.Org.ServerURL,
			JoinPolicy: req.Org.JoinPolicy, DefaultRole: req.Org.DefaultRole},
	}

	// Two ways to authenticate this call, per Appendix A: an existing
	// session obtained with the initial password, or the admin_password
	// state with the initial password supplied as current_password.
	if auth, authErr := s.identities.authenticate(r); authErr == nil {
		storeReq.ActorUserID = auth.user.ID
	} else if !errors.Is(authErr, serverauth.ErrNoCredentials) {
		writeAccessError(w, authErr)
		return
	} else {
		if req.Admin.CurrentPassword == "" {
			writeCodedError(w, http.StatusUnauthorized, "authentication required", "unauthenticated")
			return
		}
		storeReq.CurrentUsername = req.Admin.Username
		storeReq.CurrentPassword = req.Admin.CurrentPassword
	}

	user, org, err := s.identities.completeOnboardingAdmin(storeReq)
	if err != nil {
		switch {
		case errors.Is(err, errPasswordTooShort):
			writeCodedError(w, http.StatusBadRequest, "password must be at least 12 characters", "password_too_short")
		case errors.Is(err, errSingleOrg):
			writeCodedError(w, http.StatusConflict, "an organization already exists", "single_org")
		case errors.Is(err, serverauth.ErrUnauthenticated):
			writeCodedError(w, http.StatusUnauthorized, "invalid current password", "invalid_credentials")
		case errors.Is(err, serverauth.ErrForbidden):
			writeCodedError(w, http.StatusForbidden, "admin onboarding is no longer available", "forbidden")
		default:
			writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		}
		return
	}
	session, csrf, err := s.identities.createSession(user.ID)
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "onboarding failed", "internal")
		return
	}
	s.refreshIdentityProviders()
	setSessionCookie(w, session, sessionLifetime)
	writeJSON(w, http.StatusCreated, map[string]any{"user": user, "csrf": csrf, "org": org})
}

// ---- organizations -----------------------------------------------------

func isOrgRoute(path string) bool {
	return strings.HasPrefix(path, "/api/v1/orgs/")
}

func (s *Server) handleOrgsCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if _, err := s.require(w, r); err != nil {
		return
	}
	// Self-hosted supports exactly one organization, created by
	// POST /api/v1/onboarding/admin; this route only ever reports the
	// conflict, per Appendix A ("self-hosted returns 409 single_org").
	writeCodedError(w, http.StatusConflict, "self-hosted supports a single organization", "single_org")
}

func (s *Server) handleOrgRoute(w http.ResponseWriter, r *http.Request, path string) {
	rest := strings.TrimPrefix(path, "/api/v1/orgs/")
	segs := strings.Split(rest, "/")
	if len(segs) == 0 || segs[0] == "" {
		writeCodedError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	slug := segs[0]
	switch {
	case len(segs) == 1:
		s.handleOrgItem(w, r, slug)
	case len(segs) == 2 && segs[1] == "onboarding":
		s.handleOrgOnboarding(w, r, slug)
	case len(segs) == 2 && segs[1] == "auth-methods":
		s.handleOrgAuthMethods(w, r, slug)
	case len(segs) == 2 && segs[1] == "setup-codes":
		s.handleOrgSetupCodes(w, r, slug)
	case len(segs) == 2 && segs[1] == "connections":
		s.handleOrgConnections(w, r, slug)
	case len(segs) == 2 && segs[1] == "invitations":
		s.handleOrgInvitations(w, r, slug)
	case len(segs) == 3 && segs[1] == "invitations":
		s.handleOrgInvitationItem(w, r, slug, segs[2])
	case len(segs) == 2 && segs[1] == "members":
		s.handleOrgMembers(w, r, slug)
	case len(segs) == 3 && segs[1] == "members":
		s.handleOrgMemberItem(w, r, slug, segs[2])
	default:
		writeCodedError(w, http.StatusNotFound, "not found", "not_found")
	}
}

// orgAuth authenticates the caller and loads the named organization,
// requiring org-admin (or instance-owner) privilege when requireAdmin is
// true. It writes the error response itself on failure.
func (s *Server) orgAuth(w http.ResponseWriter, r *http.Request, slug string, requireAdmin bool) (authenticated, Organization, bool) {
	auth, err := s.require(w, r)
	if err != nil {
		return authenticated{}, Organization{}, false
	}
	org, err := s.identities.getOrganizationBySlug(slug)
	if err != nil {
		writeCodedError(w, http.StatusNotFound, "not found", "not_found")
		return authenticated{}, Organization{}, false
	}
	if requireAdmin && !auth.user.InstanceOwner && auth.user.OrgRole != "admin" {
		writeCodedError(w, http.StatusForbidden, "forbidden", "forbidden")
		return authenticated{}, Organization{}, false
	}
	return auth, org, true
}

func (s *Server) handleOrgItem(w http.ResponseWriter, r *http.Request, slug string) {
	switch r.Method {
	case http.MethodGet:
		_, org, ok := s.orgAuth(w, r, slug, false)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, org)
	case http.MethodPatch:
		auth, _, ok := s.orgAuth(w, r, slug, true)
		if !ok {
			return
		}
		var req struct {
			DisplayName       *string   `json:"display_name"`
			Slug              *string   `json:"slug"`
			ServerURL         *string   `json:"server_url"`
			JoinPolicy        *string   `json:"join_policy"`
			DefaultRole       *string   `json:"default_role"`
			AllowedGitHubOrgs *[]string `json:"allowed_github_orgs"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		updated, err := s.identities.updateOrganization(auth.user.ID, slug, orgPatch{
			DisplayName: req.DisplayName, Slug: req.Slug, ServerURL: req.ServerURL,
			JoinPolicy: req.JoinPolicy, DefaultRole: req.DefaultRole, AllowedGitHubOrgs: req.AllowedGitHubOrgs,
		})
		if err != nil {
			writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		// join_policy and allowed_github_orgs feed identityResolver's
		// admission rules directly (see providers.go); the resolver closes
		// over an Organization snapshot taken when the OAuth handler was
		// last built, so a change here must trigger a rebuild or it would
		// silently keep enforcing the old policy until something else
		// happened to rebuild it (e.g. a later auth-methods change).
		s.refreshIdentityProviders()
		writeJSON(w, http.StatusOK, updated)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPatch)
	}
}

func (s *Server) handleOrgOnboarding(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	auth, _, ok := s.orgAuth(w, r, slug, true)
	if !ok {
		return
	}
	var req struct {
		State string `json:"state"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	org, err := s.identities.advanceOnboarding(auth.user.ID, slug, req.State)
	if err != nil {
		if errors.Is(err, errOnboardingBackwards) {
			writeCodedError(w, http.StatusBadRequest, "onboarding state only moves forward", "invalid_transition")
			return
		}
		writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	writeJSON(w, http.StatusOK, org)
}

func (s *Server) handleOrgAuthMethods(w http.ResponseWriter, r *http.Request, slug string) {
	switch r.Method {
	case http.MethodGet:
		auth, org, ok := s.orgAuth(w, r, slug, true)
		if !ok {
			return
		}
		_ = auth
		settings, err := s.identities.getAuthMethodSettings(org.id, org.ServerURL)
		if err != nil {
			writeCodedError(w, http.StatusInternalServerError, "read auth methods failed", "internal")
			return
		}
		writeJSON(w, http.StatusOK, settings)
	case http.MethodPut:
		auth, org, ok := s.orgAuth(w, r, slug, true)
		if !ok {
			return
		}
		var req struct {
			GitHub struct {
				Enabled      *bool   `json:"enabled"`
				ClientID     *string `json:"client_id"`
				ClientSecret *string `json:"client_secret"`
				BaseURL      *string `json:"base_url"`
			} `json:"github"`
			Google struct {
				Enabled      *bool   `json:"enabled"`
				ClientID     *string `json:"client_id"`
				ClientSecret *string `json:"client_secret"`
			} `json:"google"`
			SMTP struct {
				Enabled  *bool   `json:"enabled"`
				Host     *string `json:"host"`
				Port     *int    `json:"port"`
				Username *string `json:"username"`
				Password *string `json:"password"`
				From     *string `json:"from"`
			} `json:"smtp"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		patch := authMethodPatch{
			GitHubEnabled: req.GitHub.Enabled, GitHubClientID: req.GitHub.ClientID,
			GitHubClientSecret: req.GitHub.ClientSecret, GitHubBaseURL: req.GitHub.BaseURL,
			GoogleEnabled: req.Google.Enabled, GoogleClientID: req.Google.ClientID, GoogleClientSecret: req.Google.ClientSecret,
			SMTPEnabled: req.SMTP.Enabled, SMTPHost: req.SMTP.Host, SMTPPort: req.SMTP.Port,
			SMTPUsername: req.SMTP.Username, SMTPPassword: req.SMTP.Password, SMTPFrom: req.SMTP.From,
		}
		if err := s.identities.putAuthMethodSettings(auth.user.ID, org.id, patch, s.secrets); err != nil {
			writeCodedError(w, http.StatusInternalServerError, "update auth methods failed", "internal")
			return
		}
		s.refreshIdentityProviders()
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

func (s *Server) handleOrgSetupCodes(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	auth, org, ok := s.orgAuth(w, r, slug, true)
	if !ok {
		return
	}
	code, expires, err := s.identities.createSetupCode(org.id, auth.user.ID)
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "create setup code failed", "internal")
		return
	}
	command := fmt.Sprintf("curl -fsSL %s/install | sh && rgt connect %s --setup %s",
		strings.TrimRight(org.ServerURL, "/"), org.ServerURL, code)
	writeJSON(w, http.StatusCreated, map[string]any{"code": code, "expires_at": expires, "command": command})
}

func (s *Server) handleSetupCodeExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !s.setupCodeLimiter.allow(clientKey(r), time.Now()) {
		w.Header().Set("Retry-After", "60")
		writeCodedError(w, http.StatusTooManyRequests, "too many attempts", "rate_limited")
		return
	}
	var req struct {
		Code        string `json:"code"`
		MachineName string `json:"machine_name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	user, org, token, expires, err := s.identities.exchangeSetupCode(req.Code, req.MachineName)
	if err != nil {
		switch {
		case errors.Is(err, errSetupCodeExpired):
			writeCodedError(w, http.StatusBadRequest, "setup code has expired", "setup_code_expired")
		case errors.Is(err, errSetupCodeInvalid):
			writeCodedError(w, http.StatusBadRequest, "setup code is invalid", "setup_code_invalid")
		default:
			writeCodedError(w, http.StatusInternalServerError, "exchange setup code failed", "internal")
		}
		return
	}
	_ = user
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": token, "expires_at": expires, "org": org.Slug, "server_url": org.ServerURL,
	})
}

// ---- connections feed ----------------------------------------------

func (s *Server) handleOrgConnections(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	_, org, ok := s.orgAuth(w, r, slug, false)
	if !ok {
		return
	}
	var cursor int64
	_, _ = fmt.Sscanf(r.URL.Query().Get("cursor"), "%d", &cursor)

	deadline := time.Now().Add(25 * time.Second)
	for {
		rows, latest, err := s.identities.listConnectionsSince(org.id, cursor)
		if err != nil {
			writeCodedError(w, http.StatusInternalServerError, "list connections failed", "internal")
			return
		}
		if len(rows) > 0 || time.Now().After(deadline) {
			writeJSON(w, http.StatusOK, map[string]any{"connections": rows, "cursor": latest})
			return
		}
		select {
		case <-r.Context().Done():
			writeJSON(w, http.StatusOK, map[string]any{"connections": []Connection{}, "cursor": cursor})
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// onProjectEnrolled is installed as the public core's EnrollmentHook (see
// internal/server.WithEnrollmentHook): every time POST /api/v1/projects
// succeeds — whether it created a project or reused one — for a principal
// authenticated by this composition, this appends a row to the connections
// feed the wizard's screen 2 long-polls, per RFC 0005 "the server records
// the connection row for the feed when the enrollment call succeeds."
func (s *Server) onProjectEnrolled(ctx context.Context, principal serverauth.Principal, project server.Project, _ bool) {
	org, err := s.identities.getOrganization()
	if err != nil {
		return
	}
	machineName, err := s.identities.recentTokenName(principal.Subject)
	if err != nil {
		machineName = ""
	}
	if err := s.identities.recordConnection(org.id, project.ID, project.DisplayName, project.Source.Remote, machineName, principal.Subject); err != nil {
		s.logf("record connection for project %q: %v", project.ID, err)
	}
}

// ---- organization members --------------------------------------------

func (s *Server) handleOrgMembers(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	_, _, ok := s.orgAuth(w, r, slug, false)
	if !ok {
		return
	}
	members, err := s.identities.listOrgMembers()
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "list members failed", "internal")
		return
	}
	writeJSON(w, http.StatusOK, members)
}

func (s *Server) handleOrgMemberItem(w http.ResponseWriter, r *http.Request, slug, userID string) {
	if r.Method != http.MethodPatch {
		methodNotAllowed(w, http.MethodPatch)
		return
	}
	auth, _, ok := s.orgAuth(w, r, slug, true)
	if !ok {
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	if err := s.identities.setOrgRole(auth.user.ID, userID, req.Role); err != nil {
		switch {
		case errors.Is(err, errLastAdmin):
			writeCodedError(w, http.StatusBadRequest, "the last admin cannot be demoted", "last_admin")
		case errors.Is(err, serverauth.ErrNotFound):
			writeCodedError(w, http.StatusNotFound, "not found", "not_found")
		default:
			writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- invitations ------------------------------------------------------

func (s *Server) handleOrgInvitations(w http.ResponseWriter, r *http.Request, slug string) {
	switch r.Method {
	case http.MethodGet:
		_, org, ok := s.orgAuth(w, r, slug, true)
		if !ok {
			return
		}
		list, err := s.identities.listInvitations(org.id)
		if err != nil {
			writeCodedError(w, http.StatusInternalServerError, "list invitations failed", "internal")
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost:
		auth, org, ok := s.orgAuth(w, r, slug, true)
		if !ok {
			return
		}
		var req struct {
			Email    string  `json:"email"`
			Username string  `json:"username"`
			OrgRole  string  `json:"org_role"`
			Grants   []Grant `json:"grants"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		invitation, token, err := s.identities.createInvitation(org.id, auth.user.ID, invitationCreate{
			Email: req.Email, Username: req.Username, OrgRole: req.OrgRole, Grants: req.Grants,
		})
		if err != nil {
			writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		link := strings.TrimRight(org.ServerURL, "/") + "/invitations/" + token
		emailed := false
		if req.Email != "" {
			settings, sErr := s.identities.getAuthMethodSettings(org.id, org.ServerURL)
			if sErr == nil && settings.SMTP.Enabled {
				password, pErr := s.readSMTPPassword(org.id)
				if pErr == nil {
					if sendErr := sendInvitationEmail(settings, password, req.Email, org.DisplayName, link); sendErr == nil {
						emailed = true
					} else {
						s.logf("send invitation email: %v", sendErr)
					}
				}
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"id": invitation.ID, "link": link, "expires_at": invitation.ExpiresAt, "emailed": emailed,
		})
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) readSMTPPassword(orgID string) (string, error) {
	var enc []byte
	err := s.identities.db.QueryRow(`SELECT smtp_password_enc FROM auth_method_settings WHERE org_id=?`, orgID).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return s.secrets.open(enc)
}

func (s *Server) handleOrgInvitationItem(w http.ResponseWriter, r *http.Request, slug, id string) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w, http.MethodDelete)
		return
	}
	auth, org, ok := s.orgAuth(w, r, slug, true)
	if !ok {
		return
	}
	revoked, err := s.identities.revokeInvitation(auth.user.ID, org.id, id)
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "revoke invitation failed", "internal")
		return
	}
	if !revoked {
		writeCodedError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleInvitationPublic serves GET /api/v1/invitations/{token}, public.
func (s *Server) handleInvitationPublic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/api/v1/invitations/")
	if token == "" || strings.Contains(token, "/") {
		writeCodedError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	public, err := s.identities.getInvitationByToken(token)
	if err != nil {
		switch {
		case errors.Is(err, errInvitationExpired):
			writeCodedError(w, http.StatusGone, "invitation has expired", "invitation_expired")
		case errors.Is(err, errInvitationRevoked):
			writeCodedError(w, http.StatusGone, "invitation was revoked", "invitation_revoked")
		default:
			writeCodedError(w, http.StatusNotFound, "not found", "not_found")
		}
		return
	}
	writeJSON(w, http.StatusOK, public)
}

// handleInvitationAccept serves POST /api/v1/invitations/{token}/accept, public.
func (s *Server) handleInvitationAccept(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	token := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/invitations/"), "/accept")
	if token == "" || strings.Contains(token, "/") {
		writeCodedError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
		Username    string `json:"username"`
		Password    string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	user, session, csrf, org, err := s.identities.acceptInvitation(token, invitationAccept{
		DisplayName: req.DisplayName, Username: req.Username, Password: req.Password,
	})
	if err != nil {
		switch {
		case errors.Is(err, errInvitationExpired):
			writeCodedError(w, http.StatusGone, "invitation has expired", "invitation_expired")
		case errors.Is(err, errInvitationRevoked):
			writeCodedError(w, http.StatusGone, "invitation was revoked", "invitation_revoked")
		case errors.Is(err, errPasswordTooShort):
			writeCodedError(w, http.StatusBadRequest, "password must be at least 12 characters", "password_too_short")
		default:
			writeCodedError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		}
		return
	}
	setSessionCookie(w, session, sessionLifetime)
	writeJSON(w, http.StatusCreated, map[string]any{"user": user, "csrf": csrf, "org": org})
}

// ---- backup -----------------------------------------------------------

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	auth, err := s.require(w, r)
	if err != nil {
		return
	}
	if !auth.user.InstanceOwner && auth.user.OrgRole != "admin" {
		writeCodedError(w, http.StatusForbidden, "forbidden", "forbidden")
		return
	}
	tmpDir, err := os.MkdirTemp("", "regent-backup-*")
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "backup failed", "internal")
		return
	}
	defer os.RemoveAll(tmpDir)

	files := map[string]string{}
	identitySnapshot := filepath.Join(tmpDir, "identity.db")
	if err := vacuumInto(s.identities.db, identitySnapshot); err != nil {
		writeCodedError(w, http.StatusInternalServerError, "snapshot identity database failed", "internal")
		return
	}
	files["identity.db"] = identitySnapshot

	projectsPath := filepath.Join(s.dataDir, "projects.db")
	if _, statErr := os.Stat(projectsPath); statErr == nil {
		projectsDB, openErr := sql.Open("sqlite", projectsPath)
		if openErr == nil {
			projectsSnapshot := filepath.Join(tmpDir, "projects.db")
			if err := vacuumInto(projectsDB, projectsSnapshot); err == nil {
				files["projects.db"] = projectsSnapshot
			}
			_ = projectsDB.Close()
		}
	}

	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", `attachment; filename="regent-backup.tar"`)
	tw := tar.NewWriter(w)
	defer tw.Close()
	for name, path := range files {
		if err := addFileToTar(tw, name, path); err != nil {
			s.logf("write backup entry %q: %v", name, err)
			return
		}
	}
}

func vacuumInto(db *sql.DB, dest string) error {
	_, err := db.Exec(`VACUUM INTO ?`, dest)
	return err
}

func addFileToTar(tw *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = name
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}
