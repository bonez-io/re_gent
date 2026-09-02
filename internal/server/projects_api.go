package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/bonez-io/re_gent/serverauth"
)

// legacyRegistrar is implemented by the default filesystem registry so the
// core can lazily backfill a registry row for a dataDir/repos/<id> directory
// that predates the registry, or for a legacy POST /repos creation. A
// composition supplying its own ProjectRegistry is not expected to implement
// it; legacy on-disk reconciliation is then simply skipped, which is correct
// for a composition that never used flat directories in the first place.
type legacyRegistrar interface {
	ensureLegacyProject(ctx context.Context, id string) (Project, bool, error)
}

func (s *Server) ensureLegacyProject(ctx context.Context, id string) {
	reg, ok := s.registry.(legacyRegistrar)
	if !ok {
		return
	}
	if _, _, err := reg.ensureLegacyProject(ctx, id); err != nil {
		s.logf("register legacy project %q: %v", id, err)
	}
}

// projectJSON is the wire shape of a Project in the versioned project API.
type projectJSON struct {
	ID          string            `json:"id"`
	DisplayName string            `json:"display_name"`
	OrgID       string            `json:"org_id"`
	Visibility  string            `json:"visibility"`
	CreatedAt   string            `json:"created_at"`
	Source      projectSourceJSON `json:"source"`
}

type projectSourceJSON struct {
	Remote      string `json:"remote,omitempty"`
	RootCommit  string `json:"root_commit,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func toProjectJSON(p Project) projectJSON {
	return projectJSON{
		ID: p.ID, DisplayName: p.DisplayName, OrgID: p.TenantID, Visibility: p.Visibility,
		CreatedAt: p.CreatedAt.UTC().Format(rfc3339Milli),
		Source: projectSourceJSON{
			Remote: p.Source.Remote, RootCommit: p.Source.RootCommit, Fingerprint: p.Source.Fingerprint,
		},
	}
}

const rfc3339Milli = "2006-01-02T15:04:05.000Z07:00"

// handleVersionedAPI routes every "/api/v1/…" path the core itself owns:
// GET/POST /api/v1/projects, GET/POST /api/v1/orgs/{org}/projects, and
// GET/PATCH /api/v1/projects/{id}. Compositions that layer their own
// "/api/v1/…" routes on top (self-hosted's auth/users/access routes) intercept
// those paths before delegating to the core, so this only ever sees the
// project-registry family. segs[0]=="api", segs[1]=="v1".
//
// permission has already been authorized by the caller (ServeHTTP), exactly
// like every other route family.
func (s *Server) handleVersionedAPI(w http.ResponseWriter, r *http.Request, segs []string, permission serverauth.Permission) {
	switch {
	case len(segs) == 3 && segs[2] == "projects":
		s.handleProjectsCollection(w, r, permission.Resource.TenantID)
	case len(segs) == 4 && segs[2] == "projects":
		s.handleProjectItem(w, r, permission.Resource.TenantID, segs[3])
	case len(segs) == 5 && segs[2] == "orgs" && segs[4] == "projects":
		s.handleProjectsCollection(w, r, segs[3])
	default:
		writeAPIError(w, http.StatusNotFound, "not found", "not_found")
	}
}

// isVersionedProjectRoute reports whether segs names a route
// handleVersionedAPI serves, so route classification and dispatch agree on
// exactly the same set of paths.
func isVersionedProjectRoute(segs []string) bool {
	switch {
	case len(segs) == 3 && segs[2] == "projects":
		return true
	case len(segs) == 4 && segs[2] == "projects":
		return true
	case len(segs) == 5 && segs[2] == "orgs" && segs[4] == "projects":
		return true
	default:
		return false
	}
}

func (s *Server) handleProjectsCollection(w http.ResponseWriter, r *http.Request, tenantID string) {
	switch r.Method {
	case http.MethodGet:
		s.listProjects(w, r, tenantID)
	case http.MethodPost:
		s.createProject(w, r, tenantID)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request")
	}
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request, tenantID string) {
	if tenantID == "" {
		// Legacy/self-hosted single-tenant mode: a directory created before
		// the registry existed (or by a legacy POST /repos on another
		// process) must still be listed, so backfill any missing row first.
		if ids, err := s.ListRepos(); err == nil {
			for _, id := range ids {
				s.ensureLegacyProject(r.Context(), id)
			}
		}
	}
	projects, err := s.registry.List(r.Context(), tenantID)
	if err != nil {
		s.logf("list projects: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "list projects failed", "internal")
		return
	}
	filtered, err := s.filterReadableProjects(r, projects)
	if err != nil {
		s.writeVersionedAccessError(w, err)
		return
	}
	out := make([]projectJSON, 0, len(filtered))
	for _, p := range filtered {
		out = append(out, toProjectJSON(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (s *Server) filterReadableProjects(r *http.Request, projects []Project) ([]Project, error) {
	if s.access == nil {
		return projects, nil
	}
	principal, ok := serverauth.PrincipalFromContext(r.Context())
	if !ok {
		return nil, errors.New("authenticated request is missing a principal")
	}
	allowed := make([]Project, 0, len(projects))
	for _, p := range projects {
		err := s.access.Authorize(r.Context(), principal, serverauth.Permission{
			Action:   serverauth.ActionRepositoryRead,
			Resource: serverauth.Resource{Kind: "repository", RepositoryID: p.ID, TenantID: p.TenantID},
		})
		switch {
		case err == nil:
			allowed = append(allowed, p)
		case errors.Is(err, serverauth.ErrForbidden), errors.Is(err, serverauth.ErrNotFound):
			continue
		default:
			return nil, err
		}
	}
	return allowed, nil
}

type createProjectRequest struct {
	Fingerprint string `json:"fingerprint"`
	Remote      string `json:"remote"`
	RootCommit  string `json:"root_commit"`
	DisplayName string `json:"display_name"`
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request, tenantID string) {
	var req createProjectRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "decode body: "+err.Error(), "invalid_request")
		return
	}
	if req.Fingerprint != "" {
		if err := ValidateFingerprint(req.Fingerprint); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
	}
	if req.Fingerprint == "" && req.DisplayName == "" {
		writeAPIError(w, http.StatusBadRequest, "display_name is required when fingerprint is absent", "invalid_request")
		return
	}

	principal, _ := serverauth.PrincipalFromContext(r.Context())
	if err := s.limiter.Check(r.Context(), principal, serverauth.LimitRequest{Kind: serverauth.LimitKindProject, TenantID: tenantID}); err != nil {
		s.writeLimiterError(w, err)
		return
	}

	// A fingerprint that already exists in this tenant but the caller cannot
	// read is reported as fingerprint_conflict/409 rather than silently
	// returning someone else's project.
	if req.Fingerprint != "" && s.access != nil {
		if existing, err := s.registry.LookupFingerprint(r.Context(), tenantID, req.Fingerprint); err == nil {
			readErr := s.access.Authorize(r.Context(), principal, serverauth.Permission{
				Action:   serverauth.ActionRepositoryRead,
				Resource: serverauth.Resource{Kind: "repository", RepositoryID: existing.ID, TenantID: existing.TenantID},
			})
			if readErr != nil {
				writeAPIError(w, http.StatusConflict, "fingerprint already enrolled in this organization", "fingerprint_conflict")
				return
			}
		} else if !errors.Is(err, ErrProjectNotFound) {
			s.logf("lookup fingerprint: %v", err)
			writeAPIError(w, http.StatusInternalServerError, "create project failed", "internal")
			return
		}
	}

	project, created, err := s.registry.Create(r.Context(), tenantID, ProjectCreate{
		Fingerprint: req.Fingerprint, Remote: req.Remote, RootCommit: req.RootCommit, DisplayName: req.DisplayName,
	})
	if err != nil {
		s.logf("create project: %v", err)
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}

	var upstream any
	if created && project.Source.RootCommit != "" {
		if matches, err := s.registry.LookupRootCommit(r.Context(), project.Source.RootCommit); err == nil {
			for _, candidate := range matches {
				if candidate.TenantID != tenantID {
					upstream = map[string]string{"id": candidate.ID, "display_name": candidate.DisplayName}
					break
				}
			}
		}
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{
		"project":  toProjectJSON(project),
		"created":  created,
		"upstream": upstream,
	})
}

func (s *Server) handleProjectItem(w http.ResponseWriter, r *http.Request, tenantID, id string) {
	switch r.Method {
	case http.MethodGet:
		project, err := s.registry.Get(r.Context(), tenantID, id)
		if errors.Is(err, ErrProjectNotFound) && tenantID == "" {
			// Same lazy backfill as the collection GET: a pre-registry
			// directory must resolve here too.
			s.ensureLegacyProject(r.Context(), id)
			project, err = s.registry.Get(r.Context(), tenantID, id)
		}
		if errors.Is(err, ErrProjectNotFound) {
			writeAPIError(w, http.StatusNotFound, "not found", "not_found")
			return
		}
		if err != nil {
			s.logf("get project %q: %v", id, err)
			writeAPIError(w, http.StatusInternalServerError, "get project failed", "internal")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": toProjectJSON(project)})

	case http.MethodPatch:
		var req struct {
			DisplayName string `json:"display_name"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "decode body: "+err.Error(), "invalid_request")
			return
		}
		if err := s.registry.Rename(r.Context(), tenantID, id, req.DisplayName); err != nil {
			if errors.Is(err, ErrProjectNotFound) {
				writeAPIError(w, http.StatusNotFound, "not found", "not_found")
				return
			}
			writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		project, err := s.registry.Get(r.Context(), tenantID, id)
		if err != nil {
			s.logf("read renamed project %q: %v", id, err)
			writeAPIError(w, http.StatusInternalServerError, "rename project failed", "internal")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": toProjectJSON(project)})

	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPatch)
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request")
	}
}

func (s *Server) writeVersionedAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, serverauth.ErrUnauthenticated):
		w.Header().Set("WWW-Authenticate", `Bearer realm="re_gent"`)
		writeAPIError(w, http.StatusUnauthorized, "authentication required", "unauthenticated")
	case errors.Is(err, serverauth.ErrForbidden):
		writeAPIError(w, http.StatusForbidden, "forbidden", "forbidden")
	case errors.Is(err, serverauth.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "not found", "not_found")
	default:
		s.logf("authorize request: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "authorization failed", "internal")
	}
}

func (s *Server) writeLimiterError(w http.ResponseWriter, err error) {
	var quota *serverauth.ErrQuotaExceeded
	if errors.As(err, &quota) {
		writeAPIError(w, http.StatusRequestEntityTooLarge, quota.Error(), "quota_exceeded")
		return
	}
	s.logf("limiter check: %v", err)
	writeAPIError(w, http.StatusInternalServerError, "request failed", "internal")
}

// writeAPIError writes the {"error","code"} envelope used by the versioned
// project API and, for the two specific error kinds an installed Limiter or
// IngestFilter can produce, by the legacy object/ref/repos routes too.
func writeAPIError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}

// legacyDeprecationHeaders marks a /repos response as superseded by the
// versioned project API, per RFC 0004.
func legacyDeprecationHeaders(w http.ResponseWriter) {
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Link", `</api/v1/projects>; rel="successor-version"`)
}

// splitOrgSegs reports the org id inside "/api/v1/orgs/{org}/projects", or ""
// if segs does not have that shape.
func splitOrgSegs(segs []string) string {
	if len(segs) == 5 && segs[0] == "api" && segs[1] == "v1" && segs[2] == "orgs" && segs[4] == "projects" {
		return segs[3]
	}
	return ""
}
