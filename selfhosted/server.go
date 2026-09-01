package selfhosted

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bonez-io/re_gent/server"
	"github.com/bonez-io/re_gent/serverauth"
)

const (
	sessionCookieName = "__Host-regent_session"
	csrfHeaderName    = "X-Regent-CSRF"
	maxJSONBody       = 32 << 10
)

// Setup reports the one-time operator action needed by a fresh server. The
// BootstrapToken is plaintext and must only cross a protected operator
// channel. The standalone server also writes it to dataDir/bootstrap-token
// with mode 0600 and deletes that delivery file after successful setup; the
// identity database stores only its hash.
type Setup struct {
	BootstrapRequired bool
	BootstrapToken    string
}

// Server is the secure self-hosted HTTP composition. It owns identity routes
// and delegates repository/object/history routes to the public server core.
type Server struct {
	core             *server.Server
	identities       *identityStore
	bootstrapPath    string
	bootstrapLimiter *requestLimiter
	sessionLimiter   *requestLimiter
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

// New creates a secure self-hosted server rooted at dataDir. The supplied core
// options configure storage limits, skills, binaries, and logging; the access
// controller is always the persistent self-hosted policy.
func New(dataDir string, opts ...server.Option) (*Server, Setup, error) {
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
	bootstrap, required, err := identities.rotateBootstrap()
	if err != nil {
		_ = identities.close()
		return nil, Setup{}, err
	}
	bootstrapPath := filepath.Join(dataDir, "bootstrap-token")
	if required {
		if err := writeSecretFile(bootstrapPath, bootstrap); err != nil {
			_ = identities.close()
			return nil, Setup{}, fmt.Errorf("write bootstrap credential: %w", err)
		}
	} else if err := os.Remove(bootstrapPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = identities.close()
		return nil, Setup{}, fmt.Errorf("remove stale bootstrap credential: %w", err)
	}
	coreOpts := append([]server.Option{}, opts...)
	coreOpts = append(coreOpts, server.WithAccessController(identities))
	core, err := server.New(dataDir, coreOpts...)
	if err != nil {
		_ = identities.close()
		return nil, Setup{}, err
	}
	setup := Setup{BootstrapRequired: required, BootstrapToken: bootstrap}
	return &Server{
		core:             core,
		identities:       identities,
		bootstrapPath:    bootstrapPath,
		bootstrapLimiter: newRequestLimiter(10, time.Minute),
		sessionLimiter:   newRequestLimiter(60, time.Minute),
	}, setup, nil
}

// Close releases the identity database. The repository core has no open
// process-wide resource to close.
func (s *Server) Close() error { return s.identities.close() }

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	switch {
	case r.URL.Path == "/api/v1/capabilities":
		s.handleCapabilities(w, r)
	case r.URL.Path == "/api/v1/auth/bootstrap":
		s.handleBootstrap(w, r)
	case r.URL.Path == "/api/v1/auth/session":
		s.handleSession(w, r)
	case r.URL.Path == "/api/v1/auth/me":
		s.handleMe(w, r)
	case r.URL.Path == "/api/v1/auth/tokens" || strings.HasPrefix(r.URL.Path, "/api/v1/auth/tokens/"):
		s.handleTokens(w, r)
	case r.URL.Path == "/api/v1/users":
		s.handleUsers(w, r)
	default:
		if repoID, userID, ok := accessRoute(r.URL.EscapedPath()); ok {
			s.handleMembers(w, r, repoID, userID)
			return
		}
		s.core.ServeHTTP(w, r)
	}
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	required, err := s.identities.bootstrapRequired()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "capabilities unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deployment":         "self-hosted",
		"api_version":        "v1",
		"auth_methods":       []string{"pat", "browser_session"},
		"bootstrap_required": required,
		"features":           []string{"projects", "history", "skills", "users", "memberships", "personal_tokens"},
	})
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !s.bootstrapLimiter.allow(clientKey(r), time.Now()) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many bootstrap attempts")
		return
	}
	secret, ok := authorizationCredential(r, "Bootstrap")
	if !ok || s.identities.checkBootstrap(secret) != nil {
		writeAccessError(w, serverauth.ErrUnauthenticated)
		return
	}
	var request struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	user, pat, session, csrf, err := s.identities.createFirstOwner(request.Username, request.DisplayName)
	if err != nil {
		if strings.Contains(err.Error(), "already complete") || strings.Contains(err.Error(), "username") || strings.Contains(err.Error(), "display name") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "bootstrap failed")
		return
	}
	_ = os.Remove(s.bootstrapPath)
	setSessionCookie(w, session, sessionLifetime)
	writeJSON(w, http.StatusCreated, map[string]any{"viewer": user, "token": pat, "csrf_token": csrf})
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
	response := map[string]any{"viewer": auth.user, "capabilities": capabilities, "auth_method": auth.principal.AuthMethod}
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

func (s *Server) require(w http.ResponseWriter, r *http.Request) (authenticated, error) {
	auth, err := s.identities.authenticate(r)
	if err != nil {
		writeAccessError(w, err)
		return authenticated{}, err
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
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bootstrap-token-*")
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
