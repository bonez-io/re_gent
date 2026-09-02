package selfhosted

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	internalserver "github.com/bonez-io/re_gent/internal/server"
	"github.com/bonez-io/re_gent/server"
	"github.com/bonez-io/re_gent/serverauth"
)

const (
	sessionCookieName = "__Host-regent_session"
	csrfHeaderName    = "X-Regent-CSRF"
	maxJSONBody       = 32 << 10
)

// Setup reports the one-time operator action needed by a fresh server, per
// RFC 0005 step 0. AdminPassword is the plaintext initial password and is
// only ever populated on the run that generated it (Generated is true then):
// a restart against an existing data directory never has the plaintext to
// report, only whether it is still in force (PasswordChangeRequired).
type Setup struct {
	AdminUsername          string
	AdminPassword          string
	Generated              bool
	PasswordChangeRequired bool
}

// Server is the secure self-hosted HTTP composition. It owns identity,
// onboarding, and organization routes and delegates repository/object/
// history routes to the public server core.
type Server struct {
	core       *server.Server
	identities *identityStore
	dataDir    string
	secrets    *secretBox
	logger     *log.Logger

	oauthStateKey []byte

	loginIPLimiter   *requestLimiter
	loginUserLimiter *requestLimiter
	sessionLimiter   *requestLimiter
	setupCodeLimiter *requestLimiter

	identityProvidersMu sync.RWMutex
	identityProviders   http.Handler
}

type requestLimiter struct {
	mu      sync.Mutex
	entries map[string]rateWindow
	limit   int
	window  time.Duration
}

type rateWindow struct {
	started time.Time
	count   int
}

// New creates a secure self-hosted server rooted at dataDir. adminUsername and
// adminPassword override the RFC 0005 step-0 defaults ("admin" and a random
// 20-character password); pass "" for either to keep the default. The
// supplied core options configure storage limits, skills, binaries, and
// logging; the access controller is always the persistent self-hosted policy.
func New(dataDir, adminUsername, adminPassword string, opts ...server.Option) (*Server, Setup, error) {
	if dataDir == "" {
		return nil, Setup{}, errors.New("selfhosted: data dir must not be empty")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, Setup{}, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return nil, Setup{}, fmt.Errorf("protect data directory: %w", err)
	}
	identities, err := openIdentityStore(filepath.Join(dataDir, "identity.db"))
	if err != nil {
		return nil, Setup{}, err
	}
	admin, err := identities.ensureInitialAdmin(adminUsername, adminPassword)
	if err != nil {
		_ = identities.close()
		return nil, Setup{}, err
	}
	secrets, err := openSecretBox(dataDir)
	if err != nil {
		_ = identities.close()
		return nil, Setup{}, err
	}
	oauthStateKey, err := loadOrCreateKey(filepath.Join(dataDir, "oauth-state.key"), 32)
	if err != nil {
		_ = identities.close()
		return nil, Setup{}, err
	}

	srv := &Server{
		identities:    identities,
		dataDir:       dataDir,
		secrets:       secrets,
		oauthStateKey: oauthStateKey,
		logger:        log.New(os.Stderr, "selfhosted: ", log.LstdFlags),
	}
	coreOpts := append([]server.Option{}, opts...)
	coreOpts = append(coreOpts,
		server.WithAccessController(identities),
		// identityStore implements serverauth.Auditor (Record) by writing
		// into the same audit_events table its own direct routes use, so
		// core-driven mutations and denials land in one audit trail.
		server.WithAuditor(identities),
		server.WithCapabilities(srv.capabilitiesDocument),
		internalserver.WithEnrollmentHook(srv.onProjectEnrolled),
	)
	core, err := server.New(dataDir, coreOpts...)
	if err != nil {
		_ = identities.close()
		return nil, Setup{}, err
	}
	srv.core = core
	srv.loginIPLimiter = newRequestLimiter(10, time.Minute)
	srv.loginUserLimiter = newRequestLimiter(10, time.Minute)
	srv.sessionLimiter = newRequestLimiter(60, time.Minute)
	srv.setupCodeLimiter = newRequestLimiter(20, time.Minute)
	srv.refreshIdentityProviders()

	setup := Setup{
		AdminUsername:          admin.Username,
		AdminPassword:          admin.Password,
		Generated:              admin.Generated,
		PasswordChangeRequired: admin.PasswordChangeNeeded,
	}
	return srv, setup, nil
}

// Close releases the identity database. The repository core has no open
// process-wide resource to close.
func (s *Server) Close() error { return s.identities.close() }

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	path := r.URL.Path
	switch {
	// ---- public routes (RFC 0005 Appendix A) ----
	case path == "/api/v1/auth/login":
		s.handleLogin(w, r)
	case path == "/api/v1/auth/setup-code":
		s.handleSetupCodeExchange(w, r)
	case path == "/api/v1/invitations" || (strings.HasPrefix(path, "/api/v1/invitations/") && !strings.HasSuffix(path, "/accept")):
		s.handleInvitationPublic(w, r)
	case strings.HasPrefix(path, "/api/v1/invitations/") && strings.HasSuffix(path, "/accept"):
		s.handleInvitationAccept(w, r)
	case isProviderRoute(path):
		if !s.serveIdentityProvider(w, r) {
			writeError(w, http.StatusNotFound, "not found")
		}

	// ---- authenticated auth/session routes ----
	case path == "/api/v1/auth/session":
		s.handleSession(w, r)
	case path == "/api/v1/auth/me":
		s.handleMe(w, r)
	case path == "/api/v1/auth/password":
		s.handleChangePassword(w, r)
	case path == "/api/v1/auth/logout":
		s.handleLogout(w, r)
	case path == "/api/v1/auth/tokens" || strings.HasPrefix(path, "/api/v1/auth/tokens/"):
		s.handleTokens(w, r)
	case path == "/api/v1/users":
		s.handleUsers(w, r)

	// ---- onboarding and organization routes ----
	case path == "/api/v1/onboarding/admin":
		s.handleOnboardingAdmin(w, r)
	case path == "/api/v1/orgs":
		s.handleOrgsCollection(w, r)
	case isOrgRoute(path):
		s.handleOrgRoute(w, r, path)

	// ---- admin ----
	case path == "/api/v1/admin/backup":
		s.handleBackup(w, r)

	default:
		if repoID, userID, ok := accessRoute(r.URL.EscapedPath()); ok {
			s.handleMembers(w, r, repoID, userID)
			return
		}
		s.core.ServeHTTP(w, r)
	}
}

// isProviderRoute reports whether path is
// "/api/v1/auth/{provider}/start" or "/api/v1/auth/{provider}/callback",
// the two routes identity.Handlers recognizes when mounted at
// "/api/v1/auth".
func isProviderRoute(path string) bool {
	const prefix = "/api/v1/auth/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	segs := strings.Split(rest, "/")
	if len(segs) != 2 {
		return false
	}
	return segs[1] == "start" || segs[1] == "callback"
}

// capabilitiesDocument builds the GET /api/v1/capabilities response. It is
// installed into the public server core via server.WithCapabilities, so the
// core itself serves the route publicly (before authentication, like
// /healthz) rather than selfhosted intercepting the path — the RFC 0004
// "capabilities becomes composition-provided" seam.
func (s *Server) capabilitiesDocument(*http.Request) map[string]any {
	authMethods := []string{"password", "browser_session"}
	authStarts := map[string]string{}
	features := []string{"projects", "history", "skills", "users", "memberships", "personal_tokens",
		"project_ids", "organizations", "invitations", "setup_codes"}

	doc := map[string]any{
		"deployment":   "self-hosted",
		"api_version":  "v1",
		"auth_methods": authMethods,
		"auth_starts":  authStarts,
		"features":     features,
	}

	org, err := s.identities.getOrganization()
	if errors.Is(err, errNoOrganization) {
		doc["onboarding"] = "admin_password"
		return doc
	}
	if err != nil {
		doc["onboarding"] = "admin_password"
		return doc
	}
	if org.Onboarding != "done" {
		doc["onboarding"] = org.Onboarding
	}
	settings, err := s.identities.getAuthMethodSettings(org.id, org.ServerURL)
	if err == nil {
		if settings.GitHub.Enabled {
			authMethods = append(authMethods, "github")
			authStarts["github"] = "/api/v1/auth/github/start"
		}
		if settings.Google.Enabled {
			authMethods = append(authMethods, "google")
			authStarts["google"] = "/api/v1/auth/google/start"
		}
	}
	doc["auth_methods"] = authMethods
	doc["auth_starts"] = authStarts
	return doc
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		if !s.sessionLimiter.allow(clientKey(r), time.Now()) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "too many login attempts")
			return
		}
		if _, ok := authorizationCredential(r, "Bearer"); !ok {
			writeAccessError(w, serverauth.ErrUnauthenticated)
			return
		}
		auth, err := s.identities.authenticate(r)
		if err != nil || auth.tokenKind != "pat" {
			writeAccessError(w, serverauth.ErrUnauthenticated)
			return
		}
		session, csrf, err := s.identities.createSession(auth.user.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "create session failed")
			return
		}
		setSessionCookie(w, session, sessionLifetime)
		writeJSON(w, http.StatusCreated, map[string]any{"viewer": auth.user, "csrf_token": csrf})
	case http.MethodDelete:
		auth, err := s.require(w, r)
		if err != nil {
			return
		}
		if auth.tokenKind != "session" {
			writeError(w, http.StatusBadRequest, "logout requires a browser session")
			return
		}
		if err := s.identities.revokeSession(auth); err != nil {
			writeError(w, http.StatusInternalServerError, "logout failed")
			return
		}
		clearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w, http.MethodPost, http.MethodDelete)
	}
}

// handleMe serves GET /api/v1/auth/me per RFC 0005 Appendix A:
// {user:{id, username, display_name, email}, orgs:[{slug, display_name,
// role, onboarding}], last_org}. Self-hosted has at most one organization, so
// orgs has zero or one entries and last_org is that org's slug (or "" before
// screen 1 has been saved). capabilities/auth_method/csrf_token are additive
// fields the CLI and UI already depend on.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	auth, err := s.require(w, r)
	if err != nil {
		return
	}
	if !s.authorize(w, r, auth, serverauth.ActionIdentityRead, "", auth.user.ID) {
		return
	}
	capabilities := []string{"projects:read", "history:read", "tokens:manage"}
	if auth.user.InstanceOwner {
		capabilities = append(capabilities, "projects:create", "users:manage", "memberships:manage")
	}
	orgs := []map[string]any{}
	lastOrg := ""
	if org, err := s.identities.getOrganization(); err == nil {
		role := auth.user.OrgRole
		if role == "" {
			role = "member"
		}
		orgs = append(orgs, map[string]any{
			"slug": org.Slug, "display_name": org.DisplayName, "role": role, "onboarding": org.Onboarding,
		})
		lastOrg = org.Slug
	} else if !errors.Is(err, errNoOrganization) {
		writeError(w, http.StatusInternalServerError, "read organization failed")
		return
	}
	response := map[string]any{
		"user": auth.user,
		// "viewer" duplicates "user" for one transitional release: it is the
		// pre-RFC-0005 field name internal/cli/auth.go (stream S2) still
		// reads to confirm a credential after `rgt auth login`. RFC 0005
		// Appendix A's contract is "user"; keep this alias only until S2
		// switches its decode to "user" (see the report from this stream for
		// the coordination note), then remove it.
		"viewer":       auth.user,
		"orgs":         orgs,
		"last_org":     lastOrg,
		"capabilities": capabilities,
		"auth_method":  auth.principal.AuthMethod,
	}
	if auth.tokenKind == "session" {
		response["csrf_token"] = auth.csrf
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	auth, err := s.require(w, r)
	if err != nil {
		return
	}
	tokenAction := serverauth.ActionTokenRead
	if r.Method != http.MethodGet {
		tokenAction = serverauth.ActionTokenWrite
	}
	if !s.authorize(w, r, auth, tokenAction, "", auth.user.ID) {
		return
	}
	if r.URL.Path == "/api/v1/auth/tokens" {
		switch r.Method {
		case http.MethodGet:
			tokens, err := s.identities.listTokens(auth.user.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "list tokens failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
		case http.MethodPost:
			var request struct {
				Name          string `json:"name"`
				ExpiresInDays int    `json:"expires_in_days"`
			}
			if err := decodeJSON(r, &request); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if request.ExpiresInDays == 0 {
				request.ExpiresInDays = 30
			}
			token, secret, err := s.identities.issuePAT(auth.user.ID, auth.user.ID, request.Name, time.Duration(request.ExpiresInDays)*24*time.Hour)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"token": token, "secret": secret})
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
		return
	}
	if r.Method != http.MethodDelete {
		methodNotAllowed(w, http.MethodDelete)
		return
	}
	tokenID := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/tokens/")
	if tokenID == "" || strings.Contains(tokenID, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	revoked, err := s.identities.revokeToken(auth.user.ID, auth.user.ID, tokenID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "revoke token failed")
		return
	}
	if !revoked {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	auth, err := s.require(w, r)
	if err != nil {
		return
	}
	userAction := serverauth.ActionUserList
	if r.Method != http.MethodGet {
		userAction = serverauth.ActionUserCreate
	}
	if !s.authorize(w, r, auth, userAction, "", "") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		users, err := s.identities.listUsers()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list users failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"users": users})
	case http.MethodPost:
		var request struct {
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
			Repository  string `json:"repo_id"`
			Role        Role   `json:"role"`
		}
		if err := decodeJSON(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if (request.Repository == "") != (request.Role == "") {
			writeError(w, http.StatusBadRequest, "repo_id and role must be provided together")
			return
		}
		if request.Repository != "" && !s.repoExists(request.Repository) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		user, secret, err := s.identities.createUser(auth.user.ID, request.Username, request.DisplayName, request.Repository, request.Role)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"user": user, "initial_token": secret})
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request, repoID, userID string) {
	auth, err := s.require(w, r)
	if err != nil {
		return
	}
	if !s.repoExists(repoID) {
		writeAccessError(w, serverauth.ErrNotFound)
		return
	}
	if r.Method == http.MethodGet && userID == "" {
		if !s.authorize(w, r, auth, serverauth.ActionMemberRead, repoID, userID) {
			return
		}
		members, err := s.identities.listMembers(repoID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list members failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"members": members})
		return
	}
	if !s.authorize(w, r, auth, serverauth.ActionMemberWrite, repoID, userID) {
		return
	}
	switch r.Method {
	case http.MethodPut:
		if userID != "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		var request struct {
			UserID string `json:"user_id"`
			Role   Role   `json:"role"`
		}
		if err := decodeJSON(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		currentRole, currentErr := s.identities.roleFor(repoID, request.UserID)
		if currentErr != nil && !errors.Is(currentErr, serverauth.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "read membership failed")
			return
		}
		if (request.Role == RoleOwner || currentRole == RoleOwner) && !auth.user.InstanceOwner {
			actorRole, roleErr := s.identities.roleFor(repoID, auth.user.ID)
			if roleErr != nil || actorRole != RoleOwner {
				writeAccessError(w, serverauth.ErrForbidden)
				return
			}
		}
		if err := s.identities.putMember(auth.user.ID, repoID, request.UserID, request.Role); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if userID == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		targetRole, roleErr := s.identities.roleFor(repoID, userID)
		if roleErr != nil {
			writeAccessError(w, roleErr)
			return
		}
		if targetRole == RoleOwner && !auth.user.InstanceOwner {
			actorRole, actorErr := s.identities.roleFor(repoID, auth.user.ID)
			if actorErr != nil || actorRole != RoleOwner {
				writeAccessError(w, serverauth.ErrForbidden)
				return
			}
		}
		deleted, err := s.identities.deleteMember(auth.user.ID, repoID, userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "remove member failed")
			return
		}
		if !deleted {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPut, http.MethodDelete)
	}
}

// passwordChangeExemptPaths are the "auth routes" RFC 0005 keeps reachable
// while the admin's initial password is still in force: everything else
// (via this helper — core-delegated object/ref/history/project routes are
// gated separately, in identityStore.Authorize) returns 403
// password_change_required until POST /api/v1/auth/password or
// POST /api/v1/onboarding/admin replaces it.
var passwordChangeExemptPaths = map[string]bool{
	"/api/v1/auth/me":          true,
	"/api/v1/auth/password":    true,
	"/api/v1/auth/logout":      true,
	"/api/v1/auth/session":     true,
	"/api/v1/onboarding/admin": true,
}

func (s *Server) require(w http.ResponseWriter, r *http.Request) (authenticated, error) {
	auth, err := s.identities.authenticate(r)
	if err != nil {
		writeAccessError(w, err)
		return authenticated{}, err
	}
	if auth.passwordChangeRequired && !passwordChangeExemptPaths[r.URL.Path] {
		writeCodedError(w, http.StatusForbidden, "the initial password must be replaced before this route can be used", "password_change_required")
		return authenticated{}, serverauth.ErrForbidden
	}
	return auth, nil
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request, auth authenticated, action serverauth.Action, repoID, name string) bool {
	err := s.identities.Authorize(r.Context(), auth.principal, serverauth.Permission{Action: action, Resource: serverauth.Resource{Kind: "self-hosted", RepositoryID: repoID, Name: name}})
	if err != nil {
		writeAccessError(w, err)
		return false
	}
	return true
}

func (s *Server) repoExists(repoID string) bool {
	ids, err := s.core.ListRepos()
	if err != nil {
		return false
	}
	for _, id := range ids {
		if id == repoID {
			return true
		}
	}
	return false
}

func accessRoute(escapedPath string) (repoID, userID string, ok bool) {
	parts := strings.Split(strings.Trim(escapedPath, "/"), "/")
	if len(parts) != 5 && len(parts) != 6 {
		return "", "", false
	}
	if parts[1] != "api" || parts[2] != "v1" || parts[3] != "access" || parts[4] != "members" {
		return "", "", false
	}
	decoded, err := url.PathUnescape(parts[0])
	if err != nil || strings.ContainsAny(decoded, "/\\") || server.ValidateRepoID(decoded) != nil {
		return "", "", false
	}
	if len(parts) == 6 {
		userID, err = url.PathUnescape(parts[5])
		if err != nil || userID == "" || strings.ContainsAny(userID, "/\\") {
			return "", "", false
		}
	}
	return decoded, userID, true
}

func authorizationCredential(r *http.Request, scheme string) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	parts := strings.Fields(values[0])
	returnValue := len(parts) == 2 && strings.EqualFold(parts[0], scheme) && parts[1] != ""
	if !returnValue {
		return "", false
	}
	return parts[1], true
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("decode body: expected one JSON object")
	}
	return nil
}

func setSessionCookie(w http.ResponseWriter, secret string, lifetime time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: secret, Path: "/", MaxAge: int(lifetime.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func writeAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPasswordChangeRequired):
		writeCodedError(w, http.StatusForbidden, "the initial password must be replaced before this route can be used", "password_change_required")
	case errors.Is(err, serverauth.ErrUnauthenticated):
		w.Header().Set("WWW-Authenticate", `Bearer realm="re_gent"`)
		writeError(w, http.StatusUnauthorized, "authentication required")
	case errors.Is(err, serverauth.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, serverauth.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		writeError(w, http.StatusInternalServerError, "authorization failed")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeCodedError writes the {"error","code"} envelope RFC 0005 Appendix A
// uses for the onboarding/organization API family (e.g.
// "setup_code_invalid", "password_change_required"), distinct from the
// legacy identity routes' plain {"error"} body.
func writeCodedError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func newRequestLimiter(limit int, window time.Duration) *requestLimiter {
	return &requestLimiter{entries: make(map[string]rateWindow), limit: limit, window: window}
}

func (l *requestLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) > 4096 {
		for candidate, entry := range l.entries {
			if now.Sub(entry.started) >= l.window {
				delete(l.entries, candidate)
			}
		}
	}
	entry := l.entries[key]
	if entry.started.IsZero() || now.Sub(entry.started) >= l.window {
		l.entries[key] = rateWindow{started: now, count: 1}
		return true
	}
	entry.count++
	l.entries[key] = entry
	return entry.count <= l.limit
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func writeSecretFile(path, secret string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".regent-secret-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := io.WriteString(tmp, secret+"\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
