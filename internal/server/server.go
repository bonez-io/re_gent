// Package server implements the re_gent HTTP object/ref server.
//
// A single Server hosts any number of repositories. Every request is scoped to
// exactly one repo by the first path segment, and each repo is backed by its
// own store.Store on disk (dataDir/repos/<repo-id>/). Objects, refs and history
// are therefore isolated per repo: an object uploaded to one repo is never
// visible from another, and identically named refs in two repos are distinct
// files that cannot collide.
//
// URL layout (relative to the server root):
//
//	GET    /repos                     list repo ids            -> {"repos":[…]}
//	POST   /repos                     create a repo            <- {"repo_id":"…"}
//	HEAD   /{repo}/objects/{hash}     object existence check   -> 200 | 404
//	GET    /{repo}/objects/{hash}     download object          -> 200 | 404
//	PUT    /{repo}/objects/{hash}     upload object            -> 201 | 200
//	GET    /{repo}/refs[?dir=…]       list refs                -> {"refs":{…}}
//	GET    /{repo}/refs/{name…}       read one ref             -> {"hash":"…"}
//	POST   /{repo}/refs/{name…}       CAS ref update           <- {"old":"…","new":"…"}
package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"lukechampine.com/blake3"

	"github.com/bonez-io/re_gent/internal/store"
	"github.com/bonez-io/re_gent/serverauth"
)

// DefaultMaxObjectBytes bounds a single uploaded object. Untrusted input must
// be size limited; 64 MiB is far above any blob capture produces.
const DefaultMaxObjectBytes int64 = 64 << 20

// maxRefNameBytes bounds a ref name so a client cannot create absurd paths.
const maxRefNameBytes = 512

// casRetries bounds how often a ref update is retried when the on-disk ref lock
// is held by a concurrent writer. Lock contention is not a CAS failure, so it
// must not be reported as one; a genuine mismatch is never retried.
const casRetries = 25

var (
	// repoIDRE keeps repo ids to characters that are safe as a single path
	// segment. Lowercase only: macOS (APFS) and Windows are case-insensitive,
	// so allowing "Alpha" and "alpha" would silently map two repo ids onto one
	// directory and break isolation.
	repoIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

	// hashRE matches a full hex-encoded BLAKE3-256 hash.
	hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

	// reservedRepoIDs are ids that must not become repo directories: "repos" is
	// the registry endpoint, and the rest are reserved device names on Windows.
	reservedRepoIDs = map[string]bool{
		"repos": true, "con": true, "prn": true, "aux": true, "nul": true,
		"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
		"com6": true, "com7": true, "com8": true, "com9": true,
		"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
		"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
	}

	// errRepoNotFound is returned when a read touches a repo that was never created.
	errRepoNotFound = errors.New("repo not found")
)

// Server is an http.Handler routing every request to a per-repo store.
type Server struct {
	dataDir        string
	maxObjectBytes int64
	binariesDir    string
	skillsDir      string
	logger         *log.Logger
	access         serverauth.Controller

	locator        StorageLocator
	auditor        serverauth.Auditor
	limiter        serverauth.Limiter
	ingestFilter   IngestFilter
	capabilitiesFn CapabilitiesFunc
	registry       ProjectRegistry
	enrollmentHook EnrollmentHook

	mu    sync.Mutex
	repos map[string]*store.Store
}

// Option configures a Server.
type Option func(*Server)

// WithAccessController installs the authentication and authorization boundary.
// A nil controller retains the legacy open behavior for loopback-only local
// development. Production entrypoints must reject non-loopback open mode.
func WithAccessController(controller serverauth.Controller) Option {
	return func(s *Server) { s.access = controller }
}

// WithSkillsDir points the skills registry at a directory of published skills,
// laid out as <name>/SKILL.md. Skills found there override the built-in ones of
// the same name, which is how a team publishes a skill without a release.
func WithSkillsDir(dir string) Option {
	return func(s *Server) { s.skillsDir = dir }
}

// WithMaxObjectBytes overrides the per-object upload limit.
func WithMaxObjectBytes(n int64) Option {
	return func(s *Server) {
		if n > 0 {
			s.maxObjectBytes = n
		}
	}
}

// WithLogger sets the logger used for server-side failures. Nil disables logging.
func WithLogger(l *log.Logger) Option {
	return func(s *Server) { s.logger = l }
}

// WithBinariesDir points GET /bin/rgt at a directory of prebuilt, per-OS/arch
// rgt binaries (named rgt_<goos>_<goarch>[.exe]) so a teammate on any platform
// can download a runnable binary — not just teammates matching the server's OS.
// Empty (the default) means the server only serves its own running executable.
func WithBinariesDir(dir string) Option {
	return func(s *Server) { s.binariesDir = dir }
}

// WithStorageLocator overrides where a project's on-disk store lives. The
// default reproduces today's dataDir/repos/<id> layout.
func WithStorageLocator(locator StorageLocator) Option {
	return func(s *Server) { s.locator = locator }
}

// WithAuditor installs the sink for mutation and denial audit events. The
// default records nothing.
func WithAuditor(auditor serverauth.Auditor) Option {
	return func(s *Server) { s.auditor = auditor }
}

// WithLimiter installs the quota/rate approval consulted before an object
// write, a ref move, and a project creation. The default allows everything.
func WithLimiter(limiter serverauth.Limiter) Option {
	return func(s *Server) { s.limiter = limiter }
}

// WithIngestFilter installs the gate consulted before an object is written.
// The default accepts every object unchanged.
func WithIngestFilter(filter IngestFilter) Option {
	return func(s *Server) { s.ingestFilter = filter }
}

// WithCapabilities installs the builder for the public GET
// /api/v1/capabilities document. The default returns the RFC 0004 open-mode
// document.
func WithCapabilities(fn CapabilitiesFunc) Option {
	return func(s *Server) { s.capabilitiesFn = fn }
}

// WithProjectRegistry overrides the project registry backing the versioned
// project API (/api/v1/projects, /api/v1/orgs/{org}/projects). The default is
// a filesystem+SQLite registry rooted at dataDir.
func WithProjectRegistry(registry ProjectRegistry) Option {
	return func(s *Server) { s.registry = registry }
}

// New creates a Server persisting repo data under dataDir, which is created if
// it does not exist.
func New(dataDir string, opts ...Option) (*Server, error) {
	if dataDir == "" {
		return nil, errors.New("server: data dir must not be empty")
	}
	srv := &Server{
		dataDir:        dataDir,
		maxObjectBytes: DefaultMaxObjectBytes,
		repos:          make(map[string]*store.Store),
	}
	for _, opt := range opts {
		opt(srv)
	}
	if srv.locator == nil {
		srv.locator = defaultStorageLocator{dataDir: dataDir}
	}
	if srv.auditor == nil {
		srv.auditor = noopAuditor{}
	}
	if srv.limiter == nil {
		srv.limiter = allowLimiter{}
	}
	if srv.ingestFilter == nil {
		srv.ingestFilter = passthroughIngestFilter{}
	}
	if srv.capabilitiesFn == nil {
		srv.capabilitiesFn = defaultCapabilities
	}
	if srv.registry == nil {
		srv.registry = newFilesystemProjectRegistry(dataDir, srv.locator)
	}
	if srv.enrollmentHook == nil {
		srv.enrollmentHook = noopEnrollmentHook
	}
	if err := os.MkdirAll(srv.reposDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	return srv, nil
}

func (s *Server) reposDir() string { return filepath.Join(s.dataDir, "repos") }

// repoDir is the on-disk root of one repo. The id is validated before this is
// called, so it is always a single safe path segment.

func (s *Server) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

// ValidateRepoID reports whether id may be used as a repo identifier.
func ValidateRepoID(id string) error {
	if !repoIDRE.MatchString(id) {
		return fmt.Errorf("invalid repo id %q: use 1-64 chars of [a-z0-9._-] starting with a letter or digit", id)
	}
	if reservedRepoIDs[id] {
		return fmt.Errorf("invalid repo id %q: reserved name", id)
	}
	// "." and ".." are already excluded by the leading-character rule, but a
	// name that is only dots would still be dangerous on some filesystems.
	if strings.Trim(id, ".") == "" {
		return fmt.Errorf("invalid repo id %q: reserved name", id)
	}
	return nil
}

// CreateRepo creates the repo's store if it does not exist yet and reports
// whether it was newly created.
func (s *Server) CreateRepo(repoID string) (bool, error) {
	if err := ValidateRepoID(repoID); err != nil {
		return false, err
	}
	dir, err := s.locator.ProjectRoot("", repoID)
	if err != nil {
		return false, fmt.Errorf("resolve project storage: %w", err)
	}
	_, statErr := os.Stat(dir)
	existed := statErr == nil
	if _, err := s.openRepoTenant("", repoID, true); err != nil {
		return false, err
	}
	return !existed, nil
}

// ListRepos returns the sorted ids of every repo hosted by this server.
func (s *Server) ListRepos() ([]string, error) {
	entries, err := os.ReadDir(s.reposDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && ValidateRepoID(e.Name()) == nil {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// openRepo returns the store for repoID in the default (legacy/self-hosted)
// tenant. When create is false and the repo does not exist yet, errRepoNotFound
// is returned instead of creating it, so reads never bring a repo into
// existence.
// openRepoTenant is openRepo scoped to a tenant, resolving storage through the
// configured StorageLocator rather than a hard-coded path. This is the one
// place the core opens a project's store, so every route goes through the
// locator.
func (s *Server) openRepoTenant(tenantID, repoID string, create bool) (*store.Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cacheKey := tenantID + "\x00" + repoID
	if st, ok := s.repos[cacheKey]; ok {
		return st, nil
	}

	dir, err := s.locator.ProjectRoot(tenantID, repoID)
	if err != nil {
		return nil, fmt.Errorf("resolve project storage: %w", err)
	}
	if _, err := os.Stat(dir); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if !create {
			return nil, errRepoNotFound
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create repo dir %q: %w", repoID, err)
		}
	}

	st, err := store.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open repo %q: %w", repoID, err)
	}
	s.repos[cacheKey] = st
	return st, nil
}

// ServeHTTP implements http.Handler.
//
// Routing is done by hand rather than with http.ServeMux because the mux
// rewrites "a/../b" and "./a" before a handler ever sees them, which would hide
// traversal attempts from validation.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = newRequestID()
	}
	w.Header().Set("X-Request-ID", requestID)
	r = r.WithContext(withRequestID(r.Context(), requestID))

	// Health check so container and orchestrator probes succeed. The exemption is
	// scoped to exactly "/healthz".
	if r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
		return
	}

	// The onboarding endpoints expose the open-source binary and a bootstrap
	// script that carries no secret, never any repo data. This is what makes
	// `curl -fsSL http://<server>/install | sh` a one-paste onboarding. See
	// install.go. The capabilities document is public for the same reason: it
	// carries deployment shape and negotiation information, never repository
	// or identity data.
	switch r.URL.Path {
	case "/install", "/install.sh":
		s.handleInstallScript(w, r)
		return
	case "/bin/rgt":
		s.handleBinary(w, r)
		return
	case "/api/v1/capabilities":
		s.handleCapabilities(w, r)
		return
	}

	var principal serverauth.Principal
	if s.access != nil {
		p, err := s.access.Authenticate(r)
		switch {
		case err == nil:
			if p.Subject == "" {
				s.logf("access controller returned an empty subject")
				httpError(w, http.StatusInternalServerError, "authorization failed")
				return
			}
			principal = p
		case errors.Is(err, serverauth.ErrNoCredentials):
			// No credentials at all, as opposed to bad ones: the core still
			// calls Authorize with the explicit anonymous principal, so a
			// policy can grant a public read. Every default policy denies it,
			// exactly as a request with bad credentials is denied below.
			principal = serverauth.Anonymous()
		default:
			s.writeAccessError(w, err)
			return
		}
		r = r.WithContext(serverauth.WithPrincipal(r.Context(), principal))
	}

	segs, err := pathSegments(r.URL.EscapedPath())
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	permission := s.permissionForRequest(r, segs)
	r = r.WithContext(withResourceTenant(r.Context(), permission.Resource.TenantID))

	if s.access != nil {
		if err := s.access.Authorize(r.Context(), principal, permission); err != nil {
			s.audit(r.Context(), principal, permission, denialOutcome(err))
			s.writeAccessError(w, err)
			return
		}
	}

	if !isMutationAction(permission.Action) {
		s.dispatch(w, r, segs, permission)
		return
	}
	// Every mutation gets an audit event regardless of outcome, so its result
	// is captured by wrapping the ResponseWriter rather than threading the
	// permission down into every handler that can write one.
	rec := &statusRecorder{ResponseWriter: w}
	s.dispatch(rec, r, segs, permission)
	outcome := "allowed"
	if rec.status >= 400 {
		outcome = "error"
	}
	s.audit(r.Context(), principal, permission, outcome)
}

// dispatch is the route table. It only ever runs after health/install/
// capabilities have been served directly and, when an access controller is
// installed, after the request has been authorized for permission.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, segs []string, permission serverauth.Permission) {
	if len(segs) == 0 {
		httpError(w, http.StatusBadRequest, "missing repo id")
		return
	}

	if segs[0] == "repos" && len(segs) == 1 {
		s.handleRepos(w, r)
		return
	}

	// The skills registry is global, not repo-scoped: a skill describes how to
	// interrogate re_gent, not one project's history. Matched before the repo
	// id is validated, so "api" is never mistaken for a repository.
	if segs[0] == "api" && len(segs) >= 2 && segs[1] == "skills" {
		s.handleSkills(w, r, segs)
		return
	}

	if segs[0] == "api" && len(segs) >= 2 && segs[1] == "v1" && isVersionedProjectRoute(segs) {
		s.handleVersionedAPI(w, r, segs, permission)
		return
	}

	repoID := segs[0]
	if err := ValidateRepoID(repoID); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	switch {
	case len(segs) >= 2 && segs[1] == "api":
		s.handleAPI(w, r, repoID, segs)
	case len(segs) == 3 && segs[1] == "objects":
		s.handleObject(w, r, repoID, store.Hash(segs[2]))
	case len(segs) == 2 && segs[1] == "refs":
		s.handleRefList(w, r, repoID)
	case len(segs) > 2 && segs[1] == "refs":
		s.handleRef(w, r, repoID, strings.Join(segs[2:], "/"))
	default:
		httpError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet)
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, s.capabilitiesFn(r))
}

func (s *Server) writeAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, serverauth.ErrUnauthenticated):
		w.Header().Set("WWW-Authenticate", `Bearer realm="re_gent"`)
		httpError(w, http.StatusUnauthorized, "authentication required")
	case errors.Is(err, serverauth.ErrForbidden):
		httpError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, serverauth.ErrNotFound):
		httpError(w, http.StatusNotFound, "not found")
	default:
		s.logf("authorize request: %v", err)
		httpError(w, http.StatusInternalServerError, "authorization failed")
	}
}

// permissionForRequest classifies one request into the stable action/resource
// permission an access controller decides on. It is the route-policy matrix
// made executable: every route family the server exposes must be classified
// here, and anything it does not recognize stays fail-closed on ActionRequest.
//
// TenantID: an "/api/v1/orgs/{org}/…" URL prefix names its tenant explicitly.
// Every other route — including every legacy "/{project}/…" route, which has
// no room in its URL for an org segment — takes the authenticated principal's
// own TenantID. That is exactly right for self-hosted and open mode (every
// principal's TenantID is always "", so Resource.TenantID is always "" too,
// unchanged from before this field existed) and is the correct scope for a
// managed composition, whose caller is always authenticated into exactly one
// organization per request. It deliberately does not read the project
// registry on this hot path: a registry lookup on every object/ref request
// would be a needless I/O cost for a value single-tenant mode never uses.
func (s *Server) permissionForRequest(r *http.Request, segs []string) serverauth.Permission {
	tenantID := ""
	if principal, ok := serverauth.PrincipalFromContext(r.Context()); ok {
		tenantID = principal.TenantID
	}

	permission := serverauth.Permission{
		Action: serverauth.ActionRequest,
		Resource: serverauth.Resource{
			Kind: "route",
			Name: strings.Join(segs, "/"),
		},
	}
	if len(segs) == 0 {
		return permission
	}
	if segs[0] == "repos" && len(segs) == 1 {
		permission.Resource.Kind = "repositories"
		permission.Resource.TenantID = tenantID
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			permission.Action = serverauth.ActionRepositoriesList
		} else if r.Method == http.MethodPost {
			permission.Action = serverauth.ActionRepositoryCreate
		}
		return permission
	}
	if segs[0] == "api" && len(segs) >= 2 && segs[1] == "skills" {
		permission.Resource.Kind = "skill"
		if len(segs) == 2 {
			permission.Action = serverauth.ActionSkillList
			permission.Resource.Name = ""
		} else {
			permission.Action = serverauth.ActionSkillRead
			permission.Resource.Name = segs[2]
		}
		return permission
	}
	if segs[0] == "api" && len(segs) >= 2 && segs[1] == "v1" {
		if org := splitOrgSegs(segs); org != "" {
			permission.Resource.Kind = "repositories"
			permission.Resource.TenantID = org
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				permission.Action = serverauth.ActionRepositoriesList
			} else if r.Method == http.MethodPost {
				permission.Action = serverauth.ActionRepositoryCreate
			}
			return permission
		}
		if len(segs) == 3 && segs[2] == "projects" {
			permission.Resource.Kind = "repositories"
			permission.Resource.TenantID = tenantID
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				permission.Action = serverauth.ActionRepositoriesList
			} else if r.Method == http.MethodPost {
				permission.Action = serverauth.ActionRepositoryCreate
			}
			return permission
		}
		if len(segs) == 4 && segs[2] == "projects" {
			permission.Resource.Kind = "repository"
			permission.Resource.RepositoryID = segs[3]
			permission.Resource.Name = segs[3]
			permission.Resource.TenantID = tenantID
			switch r.Method {
			case http.MethodGet, http.MethodHead:
				permission.Action = serverauth.ActionRepositoryRead
			case http.MethodPatch:
				permission.Action = serverauth.ActionRepositoryWrite
			}
			return permission
		}
		// Every other "/api/v1/…" path belongs to a composition layered on
		// top of the core (self-hosted's own auth/users/access routes, which
		// intercept those paths before the core ever sees them). Nothing
		// reaches here for those in a correctly wired composition, but if it
		// ever does, ActionRequest keeps it fail-closed rather than silently
		// open.
		return permission
	}

	permission.Resource.RepositoryID = segs[0]
	permission.Resource.TenantID = tenantID
	if len(segs) < 2 {
		return permission
	}
	switch segs[1] {
	case "objects":
		permission.Resource.Kind = "object"
		if len(segs) >= 3 {
			permission.Resource.Name = segs[2]
		}
		if r.Method == http.MethodPut {
			permission.Action = serverauth.ActionObjectWrite
		} else {
			permission.Action = serverauth.ActionObjectRead
		}
	case "refs":
		permission.Resource.Kind = "ref"
		if len(segs) >= 3 {
			ref := strings.Join(segs[2:], "/")
			permission.Resource.Name = ref
			permission.Ref = ref
		}
		if r.Method == http.MethodPost {
			permission.Action = serverauth.ActionRefWrite
		} else {
			permission.Action = serverauth.ActionRefRead
		}
	case "api":
		permission.Resource.Kind = "history"
		permission.Resource.Name = strings.Join(segs[2:], "/")
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			permission.Action = serverauth.ActionHistoryRead
		} else {
			permission.Action = serverauth.ActionHistoryWrite
		}
	}
	return permission
}

// pathSegments splits an escaped URL path into decoded, validated segments.
// Decoding happens per segment so that an encoded separator (%2F) inside a ref
// name cannot silently introduce a new path segment, and so that an encoded
// traversal component (%2E%2E) is still rejected.
func pathSegments(escapedPath string) ([]string, error) {
	trimmed := strings.Trim(escapedPath, "/")
	if trimmed == "" {
		return nil, nil
	}
	raw := strings.Split(trimmed, "/")
	segs := make([]string, 0, len(raw))
	for _, seg := range raw {
		// An empty interior segment ("a//b") is malformed rather than
		// equivalent to "a/b": collapsing it would let two different URLs
		// address the same ref.
		if seg == "" {
			return nil, errors.New("empty path segment")
		}
		dec, err := url.PathUnescape(seg)
		if err != nil {
			return nil, errors.New("malformed path escaping")
		}
		if dec == "." || dec == ".." || strings.ContainsAny(dec, "/\\") || strings.ContainsRune(dec, 0) {
			return nil, fmt.Errorf("forbidden path segment %q", seg)
		}
		segs = append(segs, dec)
	}
	return segs, nil
}

// handleRepos serves the repo registry: GET lists, POST creates. Superseded by
// the versioned project API (GET/POST /api/v1/projects); kept working for the
// documented compatibility window with RFC 0004's deprecation headers.
func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	legacyDeprecationHeaders(w)
	switch r.Method {
	case http.MethodGet:
		ids, err := s.ListRepos()
		if err != nil {
			s.logf("list repos: %v", err)
			httpError(w, http.StatusInternalServerError, "list repos failed")
			return
		}
		ids, err = s.filterReadableRepos(r, ids)
		if err != nil {
			s.writeAccessError(w, err)
			return
		}
		if ids == nil {
			ids = []string{}
		}
		writeJSON(w, http.StatusOK, struct {
			Repos []string `json:"repos"`
		}{Repos: ids})

	case http.MethodPost:
		var req struct {
			RepoID string `json:"repo_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "decode body: "+err.Error())
			return
		}
		if err := ValidateRepoID(req.RepoID); err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		principal, _ := serverauth.PrincipalFromContext(r.Context())
		if err := s.limiter.Check(r.Context(), principal, serverauth.LimitRequest{Kind: serverauth.LimitKindProject, ProjectID: req.RepoID}); err != nil {
			s.writeLimiterError(w, err)
			return
		}
		created, err := s.CreateRepo(req.RepoID)
		if err != nil {
			s.logf("create repo %q: %v", req.RepoID, err)
			httpError(w, http.StatusInternalServerError, "create repo failed")
			return
		}
		s.ensureLegacyProject(r.Context(), req.RepoID)
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, struct {
			RepoID  string `json:"repo_id"`
			Created bool   `json:"created"`
		}{RepoID: req.RepoID, Created: created})

	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) filterReadableRepos(r *http.Request, ids []string) ([]string, error) {
	if s.access == nil {
		return ids, nil
	}
	principal, ok := serverauth.PrincipalFromContext(r.Context())
	if !ok {
		return nil, errors.New("authenticated request is missing a principal")
	}
	allowed := make([]string, 0, len(ids))
	for _, repoID := range ids {
		err := s.access.Authorize(r.Context(), principal, serverauth.Permission{
			Action: serverauth.ActionRepositoryRead,
			Resource: serverauth.Resource{
				Kind:         "repository",
				RepositoryID: repoID,
				TenantID:     principal.TenantID,
			},
		})
		switch {
		case err == nil:
			allowed = append(allowed, repoID)
		case errors.Is(err, serverauth.ErrForbidden), errors.Is(err, serverauth.ErrNotFound):
			continue
		default:
			return nil, err
		}
	}
	return allowed, nil
}

// handleObject serves HEAD/GET/PUT for one object inside one repo.
func (s *Server) handleObject(w http.ResponseWriter, r *http.Request, repoID string, hash store.Hash) {
	// Short prefixes are deliberately not resolved: a server must address
	// objects unambiguously, and prefix resolution would make the reply depend
	// on what else a repo happens to hold.
	if !hashRE.MatchString(string(hash)) {
		httpError(w, http.StatusBadRequest, "invalid object hash")
		return
	}

	st := s.repoForRequest(w, r, repoID)
	if st == nil {
		return
	}

	switch r.Method {
	case http.MethodHead:
		if st.ObjectExists(hash) {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}

	case http.MethodGet:
		if !st.ObjectExists(hash) {
			httpError(w, http.StatusNotFound, "object not found")
			return
		}
		data, err := st.ReadBlob(hash)
		if err != nil {
			s.logf("read object %s in %s: %v", hash, repoID, err)
			httpError(w, http.StatusInternalServerError, "read object failed")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)

	case http.MethodPut:
		s.putObject(w, r, repoID, st, hash)

	default:
		methodNotAllowed(w, http.MethodHead, http.MethodGet, http.MethodPut)
	}
}

// putObject stores an uploaded object after proving the bytes hash to the hash
// in the URL. Without that check a client could poison an address with content
// that does not belong to it.
func (s *Server) putObject(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store, hash store.Hash) {
	if st.ObjectExists(hash) {
		// Already stored: the content at a content address is fixed, so the
		// upload is a no-op. Drain a bounded amount so the connection stays
		// reusable, then report success (idempotent PUT).
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, s.maxObjectBytes))
		w.WriteHeader(http.StatusOK)
		return
	}

	body := http.MaxBytesReader(w, r.Body, s.maxObjectBytes)
	data, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("object exceeds %d byte limit", s.maxObjectBytes))
			return
		}
		httpError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	sum := blake3.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != string(hash) {
		httpError(w, http.StatusBadRequest,
			fmt.Sprintf("content hash mismatch: body hashes to %s", got))
		return
	}

	principal, _ := serverauth.PrincipalFromContext(r.Context())
	tenantID := resourceTenantFromContext(r.Context())
	if err := s.limiter.Check(r.Context(), principal, serverauth.LimitRequest{
		Kind: serverauth.LimitKindObject, TenantID: tenantID, ProjectID: repoID, Bytes: int64(len(data)),
	}); err != nil {
		s.writeLimiterError(w, err)
		return
	}

	action, reason, err := s.ingestFilter.Filter(r.Context(), principal, repoID, string(hash), data)
	if err != nil {
		s.logf("ingest filter for object %s in %s: %v", hash, repoID, err)
		httpError(w, http.StatusInternalServerError, "write object failed")
		return
	}
	if action == IngestReject {
		writeAPIError(w, http.StatusUnprocessableEntity, reason, "ingest_rejected")
		return
	}

	if _, err := st.WriteBlob(data); err != nil {
		s.logf("write object %s in %s: %v", hash, repoID, err)
		httpError(w, http.StatusInternalServerError, "write object failed")
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// handleRefList serves GET /{repo}/refs[?dir=…].
func (s *Server) handleRefList(w http.ResponseWriter, r *http.Request, repoID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	dir := r.URL.Query().Get("dir")
	if dir != "" {
		if err := validateRefName(dir); err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	st := s.repoForRequest(w, r, repoID)
	if st == nil {
		return
	}

	refs, err := st.ListRefs(dir)
	if err != nil {
		s.logf("list refs in %s: %v", repoID, err)
		httpError(w, http.StatusInternalServerError, "list refs failed")
		return
	}
	out := make(map[string]string, len(refs))
	for name, hash := range refs {
		out[filepath.ToSlash(name)] = string(hash)
	}
	writeJSON(w, http.StatusOK, struct {
		Refs map[string]string `json:"refs"`
	}{Refs: out})
}

// handleRef serves GET (read) and POST (compare-and-swap) for one named ref.
func (s *Server) handleRef(w http.ResponseWriter, r *http.Request, repoID, name string) {
	if err := validateRefName(name); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	st := s.repoForRequest(w, r, repoID)
	if st == nil {
		return
	}

	switch r.Method {
	case http.MethodGet:
		hash, err := st.ReadRef(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				httpError(w, http.StatusNotFound, "ref not found")
				return
			}
			s.logf("read ref %s in %s: %v", name, repoID, err)
			httpError(w, http.StatusInternalServerError, "read ref failed")
			return
		}
		writeJSON(w, http.StatusOK, refResponse{Hash: string(hash)})

	case http.MethodPost:
		s.postRef(w, r, repoID, st, name)

	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

type refResponse struct {
	Hash string `json:"hash"`
}

// postRef performs a compare-and-swap ref update inside one repo.
func (s *Server) postRef(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store, name string) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	if !hashRE.MatchString(req.New) {
		httpError(w, http.StatusBadRequest, "invalid new hash")
		return
	}
	if req.Old != "" && !hashRE.MatchString(req.Old) {
		httpError(w, http.StatusBadRequest, "invalid old hash")
		return
	}

	// A ref may only point at an object this repo actually holds. This is what
	// makes cross-repo bleed impossible: uploading a step to repo "alpha" does
	// not let anyone advance a ref in repo "beta" to it.
	if !st.ObjectExists(store.Hash(req.New)) {
		httpError(w, http.StatusUnprocessableEntity,
			"ref target is not an object in this repo; upload it first")
		return
	}

	principal, _ := serverauth.PrincipalFromContext(r.Context())
	tenantID := resourceTenantFromContext(r.Context())
	if err := s.limiter.Check(r.Context(), principal, serverauth.LimitRequest{
		Kind: serverauth.LimitKindRef, TenantID: tenantID, ProjectID: repoID,
	}); err != nil {
		s.writeLimiterError(w, err)
		return
	}

	switch err := s.casUpdateRef(st, name, store.Hash(req.Old), store.Hash(req.New)); {
	case err == nil:
		writeJSON(w, http.StatusOK, refResponse{Hash: req.New})
	case errors.Is(err, store.ErrRefConflict):
		current, readErr := st.ReadRef(name)
		if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
			s.logf("read ref %s in %s after conflict: %v", name, repoID, readErr)
		}
		writeJSON(w, http.StatusConflict, refResponse{Hash: string(current)})
	default:
		s.logf("update ref %s in %s: %v", name, repoID, err)
		httpError(w, http.StatusInternalServerError, "update ref failed")
	}
}

// casUpdateRef wraps store.UpdateRef, retrying only while the ref lock is held
// by another writer.
//
// store.UpdateRefWithRetry must NOT be used here: on conflict it re-reads the
// ref and retries against the *new* value, which would clobber a concurrent
// writer's update. The client's expected-old value is the whole point of CAS,
// so a genuine mismatch is reported back instead of being papered over.
func (s *Server) casUpdateRef(st *store.Store, name string, oldHash, newHash store.Hash) error {
	backoff := time.Millisecond
	for attempt := 0; ; attempt++ {
		err := st.UpdateRef(name, oldHash, newHash)
		if !errors.Is(err, store.ErrRefConflict) {
			return err
		}
		// Distinguish lock contention from a real CAS mismatch by re-reading.
		current, readErr := st.ReadRef(name)
		if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
			return readErr
		}
		if current != oldHash || attempt >= casRetries {
			return store.ErrRefConflict
		}
		time.Sleep(backoff)
		if backoff < 20*time.Millisecond {
			backoff *= 2
		}
	}
}

// repoForRequest resolves the repo store for a request, creating the repo only
// for writes. It writes the error response itself and returns nil on failure.
func (s *Server) repoForRequest(w http.ResponseWriter, r *http.Request, repoID string) *store.Store {
	create := r.Method == http.MethodPut || r.Method == http.MethodPost
	st, err := s.openRepoTenant(resourceTenantFromContext(r.Context()), repoID, create)
	if err != nil {
		if errors.Is(err, errRepoNotFound) {
			httpError(w, http.StatusNotFound, "unknown repo "+repoID)
			return nil
		}
		s.logf("open repo %q: %v", repoID, err)
		httpError(w, http.StatusInternalServerError, "open repo failed")
		return nil
	}
	return st
}

// validateRefName rejects empty, oversized and traversal-bearing ref names.
func validateRefName(name string) error {
	if name == "" {
		return errors.New("empty ref name")
	}
	if len(name) > maxRefNameBytes {
		return fmt.Errorf("ref name exceeds %d bytes", maxRefNameBytes)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid ref name segment %q", seg)
		}
	}
	if strings.ContainsAny(name, "\\\x00") {
		return errors.New("invalid character in ref name")
	}
	if strings.HasSuffix(name, ".lock") {
		return errors.New("ref name must not end in .lock")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	http.Error(w, msg, status)
}

func methodNotAllowed(w http.ResponseWriter, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	httpError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// --- Single-repo rgt remote protocol handler (from RE-7). The multi-repo
// Server above (New/ServeHTTP) is the primary API; this Handler is retained
// for the object/ref remote used by remote.HTTPRemote.
//
// It previously also served internal/push, a standalone uploader that nothing
// reached: server-mode sync inside capture is the live upload path.
// ---
// Handler returns an http.Handler backed by s that implements the rgt remote
// protocol:
//
//	HEAD   /objects/{hash}         → 200 if exists, 404 otherwise
//	PUT    /objects/{hash}         → idempotent object upload
//	GET    /refs/{name...}         → {"hash":"..."} or 404
//	POST   /refs/{name...}         → CAS ref update
//	GET    /refs?dir={dir}         → {"refs":{"name":"hash",...}}
func Handler(s *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/objects/", func(w http.ResponseWriter, r *http.Request) {
		handleObjects(w, r, s)
	})
	mux.HandleFunc("/refs", func(w http.ResponseWriter, r *http.Request) {
		handleRefsRoot(w, r, s)
	})
	mux.HandleFunc("/refs/", func(w http.ResponseWriter, r *http.Request) {
		handleNamedRef(w, r, s)
	})
	return mux
}

func handleObjects(w http.ResponseWriter, r *http.Request, s *store.Store) {
	hash := store.Hash(strings.TrimPrefix(r.URL.Path, "/objects/"))
	if hash == "" {
		http.Error(w, "missing hash", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodHead:
		if s.ObjectExists(hash) {
			w.WriteHeader(http.StatusOK)
		} else {
			http.NotFound(w, r)
		}

	case http.MethodPut:
		// Idempotent: if we already have it, skip the write.
		if s.ObjectExists(hash) {
			w.WriteHeader(http.StatusOK)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := s.WriteBlob(data); err != nil {
			http.Error(w, "write blob: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleRefsRoot(w http.ResponseWriter, r *http.Request, s *store.Store) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir := r.URL.Query().Get("dir")
	refs, err := s.ListRefs(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert to map[string]string for JSON
	out := make(map[string]string, len(refs))
	for k, v := range refs {
		out[k] = string(v)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Refs map[string]string `json:"refs"`
	}{Refs: out})
}

func handleNamedRef(w http.ResponseWriter, r *http.Request, s *store.Store) {
	name := strings.TrimPrefix(r.URL.Path, "/refs/")
	if name == "" {
		http.Error(w, "missing ref name", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h, err := s.ReadRef(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Hash string `json:"hash"`
		}{Hash: string(h)})

	case http.MethodPost:
		var req struct {
			Old string `json:"old"`
			New string `json:"new"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
			return
		}
		err := s.UpdateRef(name, store.Hash(req.Old), store.Hash(req.New))
		if err != nil {
			if errors.Is(err, store.ErrRefConflict) {
				current, _ := s.ReadRef(name)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(struct {
					Hash string `json:"hash"`
				}{Hash: string(current)})
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
