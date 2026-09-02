package selfhosted

import (
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bonez-io/re_gent/servertest"
)

// TestConformance runs the shared RFC 0003/0004 conformance suite against the
// secure self-hosted composition. The Fixture is built entirely through this
// composition's own public HTTP endpoints — bootstrap, /repos, /api/v1/users
// — exactly as the CLI and UI use them, never through selfhosted internals,
// except for AuditEvents (see below) which necessarily reaches into the
// identity database because no public route exposes audit rows yet.
func TestConformance(t *testing.T) {
	servertest.RunConformance(t, buildSelfHostedFixture)
}

// conformanceSeq gives every minted project id and username a unique suffix
// so parallel subtests (each with their own Fixture) never collide.
var conformanceSeq int64

func nextSeq() int64 { return atomic.AddInt64(&conformanceSeq, 1) }

func buildSelfHostedFixture(t *testing.T) *servertest.Fixture {
	t.Helper()
	srv, setup, err := New(t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("selfhosted.New: %v", err)
	}
	ownerAuth := bootstrapOwner(t, srv, setup)

	fx := &servertest.Fixture{
		Handler: srv,
		Credential: func(t *testing.T, tenantID, projectID, role string) string {
			t.Helper()
			requireSingleTenant(t, tenantID)
			if projectID == "" {
				// Credential(t, tenantID, "", "owner") means the
				// instance/organization-level owner credential (see
				// Fixture.Credential's doc comment). Self-hosted's only such
				// principal is the bootstrapped instance owner: POST
				// /api/v1/projects classifies as the same repository:create
				// action as legacy POST /repos, which identityStore.Authorize
				// grants only to instance_owner.
				if role != "owner" {
					t.Fatalf("selfhosted has no instance-level credential for role %q; only \"owner\" (the instance owner) is meaningful with an empty projectID", role)
				}
				return ownerAuth
			}
			return mintProjectCredential(t, srv, ownerAuth, projectID, role)
		},
		NewProject: func(t *testing.T, tenantID, displayName string) string {
			t.Helper()
			requireSingleTenant(t, tenantID)
			return createProject(t, srv, ownerAuth, displayName)
		},
		// Self-hosted has no tenant concept: one server, one set of local
		// users and projects. RunConformance treats a nil Tenants as
		// single-tenant and reports the cross-tenant subtest skipped.
		Tenants: nil,
		AuditEvents: func(t *testing.T) []map[string]any {
			t.Helper()
			return auditRows(t, srv)
		},
		Close: func() { _ = srv.Close() },
	}
	t.Cleanup(fx.Close)
	return fx
}

func requireSingleTenant(t *testing.T, tenantID string) {
	t.Helper()
	if tenantID != "" {
		t.Fatalf("selfhosted is a single-tenant composition; got tenantID %q (Fixture.Tenants is nil, so RunConformance should never pass one)", tenantID)
	}
}

// bootstrapOwner performs the RFC 0005 first-start flow exactly as the wizard
// does: log in with the initial (generated) admin password, replace it and
// create the organization via POST /api/v1/onboarding/admin, then mint a PAT
// off the resulting session, exactly as Settings > Personal access tokens
// does. It returns that PAT as a bearer credential — the conformance suite's
// "instance/organization-level owner credential" (see Fixture.Credential's
// doc comment).
func bootstrapOwner(t *testing.T, srv *Server, setup Setup) string {
	t.Helper()
	if !setup.Generated || setup.AdminPassword == "" {
		t.Fatal("fresh selfhosted server unexpectedly has no generated initial admin password")
	}
	cookie, csrf, _ := onboardAdmin(t, srv, setup, "Conformance Org "+fmt.Sprint(nextSeq()))
	return mintOwnerPAT(t, srv, cookie, csrf)
}

var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// createProject creates a repo through POST /repos, deriving a repo id from
// displayName plus a uniqueness suffix (self-hosted repo ids must be unique
// per server and are not free-form display names).
func createProject(t *testing.T, srv *Server, ownerAuth, displayName string) string {
	t.Helper()
	repoID := fmt.Sprintf("%s-%d", slugify(displayName), nextSeq())
	if len(repoID) > 64 {
		repoID = repoID[:64]
	}
	rec := serveRequest(srv, http.MethodPost, "/repos", ownerAuth, "", map[string]string{"repo_id": repoID})
	assertStatus(t, rec, http.StatusCreated)
	return repoID
}

func slugify(displayName string) string {
	slug := slugPattern.ReplaceAllString(strings.ToLower(displayName), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "project"
	}
	if len(slug) > 40 {
		slug = slug[:40]
	}
	return slug
}

// mintProjectCredential creates a fresh local user with the given role on
// projectID through POST /api/v1/users, exactly as an instance owner would
// from the UI, and returns the user's initial PAT as a bearer credential.
func mintProjectCredential(t *testing.T, srv *Server, ownerAuth, projectID, role string) string {
	t.Helper()
	username := fmt.Sprintf("cred-%s-%d", role, nextSeq())
	rec := serveRequest(srv, http.MethodPost, "/api/v1/users", ownerAuth, "", map[string]any{
		"username":     username,
		"display_name": "Conformance " + role,
		"repo_id":      projectID,
		"role":         role,
	})
	assertStatus(t, rec, http.StatusCreated)
	var created struct {
		InitialToken string `json:"initial_token"`
	}
	decodeResponse(t, rec, &created)
	if created.InitialToken == "" {
		t.Fatalf("mint %s credential on %q: response missing initial_token: %s", role, projectID, rec.Body.String())
	}
	return "Bearer " + created.InitialToken
}

// auditRows reaches into the identity database directly because self-hosted
// does not expose audit rows over any public route yet — that is precisely
// the seam gap the conformance suite's audit_completeness subtest reports.
// This is the one place this fixture builder is not driven purely through
// HTTP, and it is intentional: without it, the suite could not observe
// whether an audit row was written at all.
// auditRows selects every column the audit_events table now carries,
// including request_id/actor_kind/tenant_id/project_id: additive columns a
// migration (selfhosted/identity.go ensureAuditColumns) added once
// identityStore started implementing serverauth.Auditor for core-driven
// mutations and denials, alongside the pre-existing columns the direct
// identity routes (bootstrap, users, tokens, memberships) have always
// written via appendAuditTx.
func auditRows(t *testing.T, srv *Server) []map[string]any {
	t.Helper()
	rows, err := srv.identities.db.Query(`SELECT actor_id, action, target_type, target_id, outcome, created_at,
request_id, actor_kind, tenant_id, project_id FROM audit_events ORDER BY id`)
	if err != nil {
		t.Fatalf("query audit_events: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var actor sql.NullString
		var action, targetType, targetID, outcome, createdAt, requestID, actorKind, tenantID, projectID string
		if err := rows.Scan(&actor, &action, &targetType, &targetID, &outcome, &createdAt,
			&requestID, &actorKind, &tenantID, &projectID); err != nil {
			t.Fatalf("scan audit_events row: %v", err)
		}
		out = append(out, map[string]any{
			"actor":       actor.String,
			"action":      action,
			"target_type": targetType,
			"target_id":   targetID,
			"outcome":     outcome,
			"created_at":  createdAt,
			"request_id":  requestID,
			"actor_kind":  actorKind,
			"tenant_id":   tenantID,
			"project_id":  projectID,
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit_events: %v", err)
	}
	return out
}
