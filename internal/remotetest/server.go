// Package remotetest provides an in-memory reference implementation of the
// re_gent server's object/ref protocol, plus fault injection.
//
// It exists so that server-mode client code can be tested against the real wire
// protocol — including induced network failures — without a running server.
// Its handlers mirror the F2 demo server: same routes, same status codes, same
// compare-and-swap and reachability rules.
//
// It is a testing helper: only *_test.go files import it.
package remotetest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
)

// maxObjectSize mirrors the production server's limit.
const maxObjectSize = 50 << 20

// Fault describes an induced failure applied to one request.
type Fault struct {
	// Status, when non-zero, is returned instead of handling the request.
	Status int
	// Hangup closes the connection without a response, simulating a network
	// blip or a server that dies mid-request.
	Hangup bool
	// Truncate returns a body that is deliberately shorter than the object,
	// simulating a proxy or connection that cuts a response short.
	Truncate bool
}

// Server is an in-memory re_gent server with fault injection.
type Server struct {
	http *httptest.Server

	mu      sync.Mutex
	objects map[store.Hash][]byte
	refs    map[string]store.Hash

	// offline makes every request fail at the transport level.
	offline bool
	// faults is a queue of one-shot faults applied to subsequent requests.
	faults []Fault
	// requests counts handled requests, for asserting on retry behaviour.
	requests map[string]int
	// token, when set, is required as a bearer token.
	token string
	// extraTokens are additionally-accepted bearer tokens minted by the device
	// login and token-refresh endpoints, on top of the single token set by
	// RequireToken.
	extraTokens map[string]bool
	// expiredTokens are tokens that must be rejected with
	// {"code":"token_expired"} rather than a plain 401, so a client's
	// refresh-and-retry path has something to react to.
	expiredTokens map[string]bool

	// RFC 0004 capabilities/enrollment/auth surface. All of it defaults to
	// off/legacy: a server nobody has called EnableProjectIDs or
	// EnableDeviceAuth on behaves exactly as this fake always has, which is
	// what lets every test written before RFC 0004 keep passing unmodified.
	projectIDsEnabled bool
	deviceAuthEnabled bool

	projects          map[string]*fakeProject
	fingerprintIndex  map[string]string // "org\x00fingerprint" -> project id
	publicRootCommits map[string]string // root_commit -> project id, public projects only
	conflictedFPs     map[string]bool   // fingerprints forced to 409 fingerprint_conflict
	projectSeq        int

	deviceCodes map[string]*fakeDeviceCode
	deviceSeq   int

	refreshResults map[string]RefreshResult // refresh_token -> what to hand back

	// registeredRepos backs the legacy POST/GET /repos registration route, so
	// a test can exercise `rgt connect`'s legacy path against this same fake
	// server type rather than only a hand-rolled one-off httptest.Server.
	registeredRepos map[string]bool

	// RFC 0005 self-hosted onboarding surface. All of it defaults to off, on
	// the same opt-in pattern as EnableProjectIDs/EnableDeviceAuth above.
	setupCodesEnabled bool
	setupCodes        map[string]*fakeSetupCode
	setupCodeSeq      int

	passwordLoginEnabled bool
	passwordAccounts     map[string]*fakePasswordAccount
	passwordSessions     map[string]*fakePasswordSession
	credentialSeq        int

	backupEnabled bool
	backupContent []byte

	// recorded holds every request handled by the RFC 0005 handlers below,
	// keyed by route, so a test can assert on exactly what the client sent —
	// see RecordedRequests.
	recorded []recordedRequest
}

// fakeSetupCode is one outstanding setup code minted by MintSetupCode.
type fakeSetupCode struct {
	org, serverURL string
	used, expired  bool
}

// fakePasswordAccount is one password-login identity registered by
// EnablePasswordLogin.
type fakePasswordAccount struct {
	username, password string
	changeRequired     bool
}

// fakePasswordSession is one browser-style session created by a successful
// password login, keyed by the opaque cookie value handed back in the
// Set-Cookie header. handleCreateMachineToken looks sessions up by that
// value and checks the CSRF token recorded alongside it.
type fakePasswordSession struct {
	userID, username string
	csrf             string
}

// recordedRequest is one request captured for a test to assert on later.
type recordedRequest struct {
	Route string
	Body  map[string]any
}

// fakeProject is the fake's in-memory record of one enrolled project.
type fakeProject struct {
	id, displayName, orgID, visibility, createdAt string
	fingerprint, remote, rootCommit               string
}

// fakeDeviceCode tracks one device-login attempt's approval state.
type fakeDeviceCode struct {
	approved, denied          bool
	accessToken, refreshToken string
	expiresIn                 int
}

// RefreshResult is what /api/v1/auth/token/refresh hands back for a
// configured refresh token. See Server.SetRefreshResult.
type RefreshResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

// New starts a reference server. Callers must Close it.
func New() *Server {
	s := &Server{
		objects:  map[store.Hash][]byte{},
		refs:     map[string]store.Hash{},
		requests: map[string]int{},
	}
	s.http = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// URL is the base URL to configure a client with.
func (s *Server) URL() string { return s.http.URL }

// Close shuts the server down.
func (s *Server) Close() { s.http.Close() }

// RequireToken makes the server reject requests without this bearer token.
func (s *Server) RequireToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
}

// SetOffline simulates the server being unreachable: connections are accepted
// and then immediately dropped, which surfaces to the client as a transport
// error, exactly like a network blip.
func (s *Server) SetOffline(offline bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offline = offline
}

// InjectFaults queues one-shot faults, applied in order to the next requests.
func (s *Server) InjectFaults(faults ...Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, faults...)
}

// EnableProjectIDs makes /api/v1/capabilities report the "project_ids"
// feature and turns on the RFC 0004 project enrollment routes
// (/api/v1/projects, /api/v1/orgs/{org}/projects). Off by default: a server
// nobody calls this on is legacy, exactly as every server was before RFC
// 0004, which is what keeps every pre-existing test against this fake green.
func (s *Server) EnableProjectIDs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projectIDsEnabled = true
}

// EnableDeviceAuth makes /api/v1/capabilities list "device" among
// auth_methods and turns on the device-login routes. Off by default.
func (s *Server) EnableDeviceAuth() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deviceAuthEnabled = true
}

// EnableSetupCodes turns on POST /api/v1/auth/setup-code and lists
// "setup_codes" among capabilities features (RFC 0005 Appendix A,
// "Enrollment through a setup code"). Off by default.
func (s *Server) EnableSetupCodes() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setupCodesEnabled = true
}

// MintSetupCode creates a one-time setup code bound to org, as the wizard's
// "POST /api/v1/orgs/{slug}/setup-codes" would, and returns the code a test
// can exchange via `rgt connect --setup` or `rgt init --setup`. serverURL is
// echoed back in the exchange response's server_url field; tests typically
// pass the fake's own URL().
func (s *Server) MintSetupCode(org, serverURL string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setupCodeSeq++
	code := fmt.Sprintf("SETUP-CODE-%d", s.setupCodeSeq)
	if s.setupCodes == nil {
		s.setupCodes = map[string]*fakeSetupCode{}
	}
	s.setupCodes[code] = &fakeSetupCode{org: org, serverURL: serverURL}
	return code
}

// ConsumeSetupCode marks code already used, so the next exchange attempt
// fails exactly as a reused code does against the real server
// ("setup_code_invalid") — the helper the S2 test suite uses to prove a
// setup code is one-time.
func (s *Server) ConsumeSetupCode(code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sc, ok := s.setupCodes[code]; ok {
		sc.used = true
	}
}

// ExpireSetupCode marks code past its 15-minute window, so the next exchange
// attempt fails with "setup_code_expired" instead of "setup_code_invalid".
func (s *Server) ExpireSetupCode(code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sc, ok := s.setupCodes[code]; ok {
		sc.expired = true
	}
}

// EnablePasswordLogin turns on POST /api/v1/auth/login and the
// session-cookie-authenticated POST /api/v1/auth/tokens it feeds, and lists
// "password" among capabilities auth_methods (RFC 0005, "Step 1: sign in and
// the wizard"). changeRequired mirrors the initial-admin-password state: when
// true, the login response carries password_change_required and a caller
// must refuse to mint a machine credential. Off by default; calling it again
// for a different username adds a second account rather than replacing the
// first.
func (s *Server) EnablePasswordLogin(username, password string, changeRequired bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.passwordLoginEnabled = true
	if s.passwordAccounts == nil {
		s.passwordAccounts = map[string]*fakePasswordAccount{}
	}
	s.passwordAccounts[username] = &fakePasswordAccount{username: username, password: password, changeRequired: changeRequired}
}

// EnableBackup turns on POST /api/v1/admin/backup, returning content
// verbatim as the response body (RFC 0005 Appendix A, "Backup"). Off by
// default.
func (s *Server) EnableBackup(content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backupEnabled = true
	s.backupContent = content
}

// RecordedRequests returns the decoded JSON bodies of every request handled
// at route (e.g. "POST /api/v1/auth/setup-code"), in the order they arrived,
// so a test can assert on exactly what the client sent — that a second
// setup-code exchange named the same code, or that the machine credential's
// name carries the local hostname.
func (s *Server) RecordedRequests(route string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, rr := range s.recorded {
		if rr.Route == route {
			out = append(out, rr.Body)
		}
	}
	return out
}

// recordRequestLocked appends one request. Callers must already hold s.mu.
func (s *Server) recordRequestLocked(route string, body map[string]any) {
	s.recorded = append(s.recorded, recordedRequest{Route: route, Body: body})
}

// ExpireToken makes the fake reject token with {"code":"token_expired"}
// instead of the plain 401 an unrecognised token gets, so a test can drive
// HTTPClient's refresh-and-retry path deterministically.
func (s *Server) ExpireToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expiredTokens == nil {
		s.expiredTokens = map[string]bool{}
	}
	s.expiredTokens[token] = true
}

// SetRefreshResult configures what POST /api/v1/auth/token/refresh returns
// for refreshToken. The returned access token is accepted on subsequent
// requests as though RequireToken had named it.
func (s *Server) SetRefreshResult(refreshToken string, result RefreshResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refreshResults == nil {
		s.refreshResults = map[string]RefreshResult{}
	}
	s.refreshResults[refreshToken] = result
}

// ApproveDevice marks a device code (as returned in
// DeviceAuthorization.DeviceCode) approved, so the next poll succeeds with
// the given token pair. Until this is called, polling that device code
// reports "authorization_pending" — the default, unapproved state a real
// user has not yet acted on.
func (s *Server) ApproveDevice(deviceCode, accessToken, refreshToken string, expiresIn int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dc, ok := s.deviceCodes[deviceCode]
	if !ok {
		return
	}
	dc.approved = true
	dc.accessToken = accessToken
	dc.refreshToken = refreshToken
	dc.expiresIn = expiresIn
}

// DenyDevice marks a device code denied, so the next poll reports "denied".
func (s *Server) DenyDevice(deviceCode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if dc, ok := s.deviceCodes[deviceCode]; ok {
		dc.denied = true
	}
}

// MarkProjectPublic flips a project's visibility to public and indexes its
// root commit, so a later enrollment whose root commit matches (a fork) is
// reported back as an upstream match.
func (s *Server) MarkProjectPublic(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[projectID]
	if !ok {
		return
	}
	p.visibility = "public"
	if p.rootCommit == "" {
		return
	}
	if s.publicRootCommits == nil {
		s.publicRootCommits = map[string]string{}
	}
	s.publicRootCommits[p.rootCommit] = p.id
}

// ForceFingerprintConflict makes any enrollment attempt naming this
// fingerprint fail with 409 fingerprint_conflict, simulating "enrolled in
// this organization, but the caller lacks access to it" without this fake
// having to model organization membership.
func (s *Server) ForceFingerprintConflict(fingerprint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conflictedFPs == nil {
		s.conflictedFPs = map[string]bool{}
	}
	s.conflictedFPs[fingerprint] = true
}

// ProjectCount returns how many projects have been enrolled, for asserting
// that a repeated connect did not create a second one.
func (s *Server) ProjectCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.projects)
}

// Requests returns the number of handled requests for a method, e.g. "POST".
func (s *Server) Requests(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[method]
}

// Objects returns a copy of the stored objects.
func (s *Server) Objects() map[store.Hash][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[store.Hash][]byte, len(s.objects))
	for h, data := range s.objects {
		out[h] = data
	}
	return out
}

// Ref returns a stored ref, or "" if absent.
func (s *Server) Ref(name string) store.Hash {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refs[name]
}

// SetRef forces a ref value, for constructing divergence scenarios.
func (s *Server) SetRef(name string, h store.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refs[name] = h
}

// DropObject deletes an object, for constructing partial-write scenarios where
// a ref exists but its contents do not.
func (s *Server) DropObject(h store.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, h)
}

// nextFault pops the next queued fault, if any.
func (s *Server) nextFault() (Fault, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.faults) == 0 {
		return Fault{}, false
	}
	f := s.faults[0]
	s.faults = s.faults[1:]
	return f, true
}

func (s *Server) isOffline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offline
}

func (s *Server) authOK(r *http.Request) bool {
	presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	want := s.token
	extra := s.extraTokens[presented]
	s.mu.Unlock()
	if want == "" {
		return true
	}
	return presented == want || extra
}

// tokenExpired reports whether the request's bearer token was explicitly
// marked expired via ExpireToken. Checked ahead of authOK so the client sees
// {"code":"token_expired"} rather than the ambiguous "bad token" a wrong or
// missing token gets.
func (s *Server) tokenExpired(r *http.Request) bool {
	presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if presented == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expiredTokens[presented]
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests[r.Method]++
	s.mu.Unlock()

	if s.isOffline() {
		hangup(w)
		return
	}
	if fault, ok := s.nextFault(); ok {
		switch {
		case fault.Hangup:
			hangup(w)
			return
		case fault.Status != 0:
			writeError(w, fault.Status, "injected fault")
			return
		case fault.Truncate:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("truncated"))
			return
		}
	}

	// RFC 0004 surface. Capabilities is explicitly public; a device login has
	// no token yet by definition; a refresh call authenticates with the
	// refresh token in its body, not a bearer header. Everything else keeps
	// the pre-existing bearer-token gate below.
	switch r.URL.Path {
	case "/healthz":
		w.WriteHeader(http.StatusOK)
		return
	case "/api/v1/capabilities":
		s.handleCapabilities(w, r)
		return
	case "/api/v1/auth/device":
		s.handleDeviceStart(w, r)
		return
	case "/api/v1/auth/device/token":
		s.handleDeviceToken(w, r)
		return
	case "/api/v1/auth/token/refresh":
		s.handleTokenRefresh(w, r)
		return
	// RFC 0005 self-hosted onboarding: setup-code exchange and password
	// login are both public routes (Appendix A), so — like capabilities and
	// the device-login start above — they sit ahead of the bearer-token gate
	// below rather than requiring one.
	case "/api/v1/auth/setup-code":
		s.handleSetupCodeExchange(w, r)
		return
	case "/api/v1/auth/login":
		s.handlePasswordLogin(w, r)
		return
	}

	// Legacy repo registration (pre-RFC-0004): kept on this fake so a test can
	// exercise `rgt connect`'s repo_id path against the same server type as
	// its project_id path, rather than needing a second one-off fake.
	if r.URL.Path == "/repos" {
		if !s.authOK(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad token", "code": "unauthenticated"})
			return
		}
		s.handleRepos(w, r)
		return
	}

	// The machine-credential creation route is session-cookie-and-CSRF
	// authenticated (RFC 0005: "through the existing PAT creation route"),
	// not bearer-token authenticated like everything below this point, so it
	// is handled here — ahead of the bearer-token gate — and checks its own
	// auth inside handleCreateMachineToken.
	if r.URL.Path == "/api/v1/auth/tokens" {
		s.handleCreateMachineToken(w, r)
		return
	}

	// The backup route is bearer-token authenticated like the object/ref
	// protocol below, but it is not part of that protocol's routing (it has
	// no repo-id path segment), so it is gated and dispatched here.
	if r.URL.Path == "/api/v1/admin/backup" {
		if !s.authOK(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad token", "code": "unauthenticated"})
			return
		}
		s.handleBackup(w, r)
		return
	}

	if s.tokenExpired(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "token expired", "code": "token_expired"})
		return
	}
	if !s.authOK(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad token", "code": "unauthenticated"})
		return
	}

	if strings.HasPrefix(r.URL.Path, "/api/v1/projects") || strings.HasPrefix(r.URL.Path, "/api/v1/orgs/") {
		s.handleProjectsAPI(w, r)
		return
	}

	// Reject traversal before any path parsing, as the production server does.
	for _, seg := range strings.Split(r.URL.Path, "/") {
		if seg == "." || seg == ".." {
			writeError(w, http.StatusBadRequest, "path contains traversal sequences")
			return
		}
	}

	// Route like the production server: the first path segment is the repo id,
	// then the kind (objects|refs), then the remainder — NOT /repos/{repo}/….
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	kind, rest := parts[1], ""
	if len(parts) == 3 {
		rest = parts[2]
	}

	switch {
	case kind == "objects" && r.Method == http.MethodPut:
		s.putObject(w, r, store.Hash(rest))
	case kind == "objects" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		s.getObject(w, store.Hash(rest))
	case kind == "refs" && rest == "" && r.Method == http.MethodGet:
		s.listRefs(w, r.URL.Query().Get("dir"))
	case kind == "refs" && r.Method == http.MethodGet:
		s.getRef(w, rest)
	case kind == "refs" && r.Method == http.MethodPost:
		s.postRef(w, r, rest)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) putObject(w http.ResponseWriter, r *http.Request, hash store.Hash) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxObjectSize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	if len(data) > maxObjectSize {
		writeError(w, http.StatusRequestEntityTooLarge, "object too large")
		return
	}
	// Like the production server: the body must hash to the address in the URL.
	if got := store.HashBytes(data); got != hash {
		writeError(w, http.StatusBadRequest, "content hash mismatch")
		return
	}

	s.mu.Lock()
	_, existed := s.objects[hash]
	s.objects[hash] = data
	s.mu.Unlock()

	if existed {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

func (s *Server) getObject(w http.ResponseWriter, h store.Hash) {
	s.mu.Lock()
	data, ok := s.objects[h]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "object not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
}

// listRefs mirrors the production server's GET /{repo}/refs[?dir=…]: names come
// back relative to dir, the way store.ListRefs reports them.
func (s *Server) listRefs(w http.ResponseWriter, dir string) {
	prefix := ""
	if dir != "" {
		prefix = strings.TrimSuffix(dir, "/") + "/"
	}

	s.mu.Lock()
	out := map[string]string{}
	for name, h := range s.refs {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		out[strings.TrimPrefix(name, prefix)] = string(h)
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"refs": out})
}

func (s *Server) getRef(w http.ResponseWriter, name string) {
	s.mu.Lock()
	h, ok := s.refs[name]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "ref not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"hash": string(h)})
}

func (s *Server) postRef(w http.ResponseWriter, r *http.Request, name string) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Reachability: refuse to advance a ref to a step whose objects are absent.
	// This is what turns a partial upload into a 422 instead of a dangling ref.
	if err := s.verifyReachableLocked(store.Hash(req.New)); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	current := s.refs[name]
	if string(current) != req.Old {
		writeError(w, http.StatusConflict, "ref was modified concurrently")
		return
	}
	s.refs[name] = store.Hash(req.New)
	writeJSON(w, http.StatusOK, map[string]string{"hash": req.New})
}

// verifyReachableLocked mirrors the production server: the object must exist
// and, if it parses as a step, its tree must exist too.
func (s *Server) verifyReachableLocked(h store.Hash) error {
	data, ok := s.objects[h]
	if !ok {
		return fmt.Errorf("object %s not found", h)
	}
	var step store.Step
	if json.Unmarshal(data, &step) != nil {
		return nil
	}
	if step.Tree == "" {
		return nil
	}
	if _, ok := s.objects[step.Tree]; !ok {
		return fmt.Errorf("step %s references tree %s which has not been pushed", h, step.Tree)
	}
	return nil
}

// handleCapabilities serves the one public, unauthenticated route every
// deployment exposes (RFC 0004).
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	s.mu.Lock()
	projectIDs := s.projectIDsEnabled
	deviceAuth := s.deviceAuthEnabled
	passwordAuth := s.passwordLoginEnabled
	setupCodes := s.setupCodesEnabled
	s.mu.Unlock()

	authMethods := []string{"pat"}
	if deviceAuth {
		authMethods = append(authMethods, "device")
	}
	if passwordAuth {
		authMethods = append(authMethods, "password")
	}
	features := []string{}
	if projectIDs {
		features = append(features, "project_ids")
	}
	if setupCodes {
		features = append(features, "setup_codes")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deployment":         "self-hosted",
		"api_version":        "v1",
		"auth_methods":       authMethods,
		"bootstrap_required": false,
		"features":           features,
	})
}

func methodNotAllowed(w http.ResponseWriter, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// --- RFC 0004 device login -------------------------------------------------

func (s *Server) handleDeviceStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	s.mu.Lock()
	s.deviceSeq++
	code := fmt.Sprintf("device-code-%d", s.deviceSeq)
	userCode := fmt.Sprintf("USER-%04d", s.deviceSeq)
	if s.deviceCodes == nil {
		s.deviceCodes = map[string]*fakeDeviceCode{}
	}
	s.deviceCodes[code] = &fakeDeviceCode{}
	verificationURL := s.http.URL + "/device"
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":      code,
		"user_code":        userCode,
		"verification_url": verificationURL,
		"interval":         1,
		"expires_in":       600,
	})
}

func (s *Server) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	dc, ok := s.deviceCodes[req.DeviceCode]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "expired"})
		return
	}
	switch {
	case dc.denied:
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "denied"})
	case dc.approved:
		if s.extraTokens == nil {
			s.extraTokens = map[string]bool{}
		}
		s.extraTokens[dc.accessToken] = true
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token":  dc.accessToken,
			"refresh_token": dc.refreshToken,
			"expires_in":    dc.expiresIn,
		})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "authorization_pending"})
	}
}

func (s *Server) handleTokenRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	result, ok := s.refreshResults[req.RefreshToken]
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown refresh token", "code": "unauthenticated"})
		return
	}
	if s.extraTokens == nil {
		s.extraTokens = map[string]bool{}
	}
	s.extraTokens[result.AccessToken] = true
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  result.AccessToken,
		"refresh_token": result.RefreshToken,
		"expires_in":    result.ExpiresIn,
	})
}

// --- RFC 0005 self-hosted onboarding ----------------------------------------

// passwordSessionCookieName is this fake's session cookie. It deliberately
// does not reuse selfhosted/server.go's "__Host-regent_session" name or its
// Secure attribute: a "__Host-" cookie is only usable over https, and this
// fake serves plain http (httptest.NewServer), where Go's cookiejar drops a
// Secure cookie before ever resending it. Nothing in the client contract
// (internal/remote/password.go) depends on the cookie's name — it relies
// entirely on http.Client.Jar carrying whatever the server set — so this is a
// faithful-enough double of the mechanism without inheriting a name that
// cannot work over http.
const passwordSessionCookieName = "regent_session_fake"

func (s *Server) handleSetupCodeExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var req struct {
		Code        string `json:"code"`
		MachineName string `json:"machine_name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordRequestLocked("POST /api/v1/auth/setup-code", map[string]any{"code": req.Code, "machine_name": req.MachineName})

	if !s.setupCodesEnabled {
		http.NotFound(w, r)
		return
	}
	sc, ok := s.setupCodes[req.Code]
	if !ok || sc.used {
		writeAPIError(w, http.StatusBadRequest, "setup_code_invalid", "this setup code is invalid")
		return
	}
	if sc.expired {
		writeAPIError(w, http.StatusBadRequest, "setup_code_expired", "this setup code has expired")
		return
	}
	sc.used = true

	s.credentialSeq++
	token := fmt.Sprintf("setup-token-%d", s.credentialSeq)
	if s.extraTokens == nil {
		s.extraTokens = map[string]bool{}
	}
	s.extraTokens[token] = true

	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": time.Now().UTC().Add(90 * 24 * time.Hour).Format(time.RFC3339),
		"org":        sc.org,
		"server_url": sc.serverURL,
	})
}

func (s *Server) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordRequestLocked("POST /api/v1/auth/login", map[string]any{"username": req.Username})

	if !s.passwordLoginEnabled {
		http.NotFound(w, r)
		return
	}
	acct, ok := s.passwordAccounts[req.Username]
	if !ok || acct.password != req.Password {
		writeAPIError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}

	s.credentialSeq++
	cookieValue := fmt.Sprintf("session-%d", s.credentialSeq)
	csrf := fmt.Sprintf("csrf-%d", s.credentialSeq)
	if s.passwordSessions == nil {
		s.passwordSessions = map[string]*fakePasswordSession{}
	}
	s.passwordSessions[cookieValue] = &fakePasswordSession{
		userID:   "usr_" + acct.username,
		username: acct.username,
		csrf:     csrf,
	}

	http.SetCookie(w, &http.Cookie{
		Name:     passwordSessionCookieName,
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":           "usr_" + acct.username,
			"username":     acct.username,
			"display_name": acct.username,
			"email":        "",
		},
		"csrf":                     csrf,
		"password_change_required": acct.changeRequired,
	})
}

// handleCreateMachineToken fakes the pre-existing PAT-creation route
// (selfhosted/server.go, POST /api/v1/auth/tokens), authenticated the way
// that route actually is: a session cookie plus the CSRF header named by
// remote.csrfHeaderName ("X-Regent-CSRF") — not a bearer token, which is why
// serve() dispatches here ahead of the bearer-token gate.
func (s *Server) handleCreateMachineToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	cookie, cookieErr := r.Cookie(passwordSessionCookieName)

	s.mu.Lock()
	defer s.mu.Unlock()

	if cookieErr != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required", "code": "unauthenticated"})
		return
	}
	session, ok := s.passwordSessions[cookie.Value]
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required", "code": "unauthenticated"})
		return
	}
	if r.Header.Get("X-Regent-CSRF") != session.csrf {
		writeError(w, http.StatusForbidden, "missing or invalid CSRF token")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	s.recordRequestLocked("POST /api/v1/auth/tokens", map[string]any{"name": req.Name, "username": session.username})

	s.credentialSeq++
	secret := fmt.Sprintf("pat-%d", s.credentialSeq)
	if s.extraTokens == nil {
		s.extraTokens = map[string]bool{}
	}
	s.extraTokens[secret] = true

	writeJSON(w, http.StatusCreated, map[string]any{
		"token": map[string]any{
			"id":   fmt.Sprintf("tok_%d", s.credentialSeq),
			"name": req.Name,
		},
		"secret": secret,
	})
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	s.mu.Lock()
	enabled := s.backupEnabled
	content := s.backupContent
	if enabled {
		s.recordRequestLocked("POST /api/v1/admin/backup", map[string]any{})
	}
	s.mu.Unlock()

	if !enabled {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// --- legacy repo registration (pre-RFC-0004) --------------------------------

func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		ids := make([]string, 0, len(s.registeredRepos))
		for id := range s.registeredRepos {
			ids = append(ids, id)
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"repos": ids})
	case http.MethodPost:
		var req struct {
			RepoID string `json:"repo_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.RepoID == "" {
			writeError(w, http.StatusBadRequest, "repo_id is required")
			return
		}
		s.mu.Lock()
		if s.registeredRepos == nil {
			s.registeredRepos = map[string]bool{}
		}
		_, existed := s.registeredRepos[req.RepoID]
		s.registeredRepos[req.RepoID] = true
		s.mu.Unlock()
		status := http.StatusCreated
		if existed {
			status = http.StatusOK
		}
		writeJSON(w, status, map[string]any{"repo_id": req.RepoID, "created": !existed})
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// --- RFC 0004 project enrollment --------------------------------------------

func (s *Server) handleProjectsAPI(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	enabled := s.projectIDsEnabled
	s.mu.Unlock()
	if !enabled {
		http.NotFound(w, r)
		return
	}

	path := r.URL.Path
	switch {
	case path == "/api/v1/projects" && r.Method == http.MethodPost:
		s.createProject(w, r, "")
	case path == "/api/v1/projects" && r.Method == http.MethodGet:
		s.listProjects(w)
	case strings.HasPrefix(path, "/api/v1/projects/") && r.Method == http.MethodGet:
		s.getProject(w, strings.TrimPrefix(path, "/api/v1/projects/"))
	case strings.HasPrefix(path, "/api/v1/orgs/") && strings.HasSuffix(path, "/projects") && r.Method == http.MethodPost:
		org := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/orgs/"), "/projects")
		s.createProject(w, r, org)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request, org string) {
	var req struct {
		Fingerprint string `json:"fingerprint"`
		Remote      string `json:"remote"`
		RootCommit  string `json:"root_commit"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Fingerprint == "" && req.DisplayName == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "display_name is required when there is no fingerprint")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if req.Fingerprint != "" && s.conflictedFPs[req.Fingerprint] {
		writeAPIError(w, http.StatusConflict, "fingerprint_conflict",
			"this repository is already enrolled in this organization; ask an admin for access")
		return
	}

	// Connect-once: the same fingerprint in the same organization returns the
	// project that already exists rather than creating a duplicate.
	fpKey := org + "\x00" + req.Fingerprint
	if req.Fingerprint != "" {
		if id, ok := s.fingerprintIndex[fpKey]; ok {
			writeJSON(w, http.StatusOK, map[string]any{
				"project":  projectJSON(s.projects[id]),
				"created":  false,
				"upstream": nil,
			})
			return
		}
	}

	// A different remote with the same root commit as a known public project
	// looks like a fork (RFC 0004, "Forks and the upstream project").
	var upstream any
	if req.Fingerprint != "" && req.RootCommit != "" {
		if upstreamID, ok := s.publicRootCommits[req.RootCommit]; ok {
			if up := s.projects[upstreamID]; up != nil && up.orgID != org {
				upstream = map[string]string{"id": up.id, "display_name": up.displayName}
			}
		}
	}

	s.projectSeq++
	id := fmt.Sprintf("prj_%012d", s.projectSeq)
	displayName := req.DisplayName
	if displayName == "" {
		displayName = lastPathSegment(req.Remote)
	}
	p := &fakeProject{
		id:          id,
		displayName: displayName,
		orgID:       org,
		visibility:  "private",
		createdAt:   time.Now().UTC().Format(time.RFC3339),
		fingerprint: req.Fingerprint,
		remote:      req.Remote,
		rootCommit:  req.RootCommit,
	}
	if s.projects == nil {
		s.projects = map[string]*fakeProject{}
	}
	s.projects[id] = p
	if req.Fingerprint != "" {
		if s.fingerprintIndex == nil {
			s.fingerprintIndex = map[string]string{}
		}
		s.fingerprintIndex[fpKey] = id
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"project":  projectJSON(p),
		"created":  true,
		"upstream": upstream,
	})
}

func (s *Server) getProject(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "no such project")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": projectJSON(p)})
}

func (s *Server) listProjects(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, projectJSON(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func projectJSON(p *fakeProject) map[string]any {
	source := map[string]any{}
	if p.remote != "" {
		source["remote"] = p.remote
	}
	if p.rootCommit != "" {
		source["root_commit"] = p.rootCommit
	}
	if p.fingerprint != "" {
		source["fingerprint"] = p.fingerprint
	}
	body := map[string]any{
		"id":           p.id,
		"display_name": p.displayName,
		"org_id":       p.orgID,
		"visibility":   p.visibility,
		"created_at":   p.createdAt,
	}
	if len(source) > 0 {
		body["source"] = source
	}
	return body
}

func lastPathSegment(remote string) string {
	remote = strings.TrimSuffix(remote, "/")
	if i := strings.LastIndexByte(remote, '/'); i >= 0 {
		return remote[i+1:]
	}
	return remote
}

func writeAPIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// hangup closes the connection without writing a response, which the client
// sees as a transport error.
func hangup(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, http.StatusInternalServerError, "cannot hijack")
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	_ = conn.Close()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
