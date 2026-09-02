package server

import (
	"context"
	"net/http"
	"path/filepath"

	"github.com/bonez-io/re_gent/serverauth"
)

// StorageLocator resolves where one project's on-disk store lives. The core
// calls it every time it needs to open a project's store, instead of
// hard-coding dataDir/repos/<id>.
//
// The default implementation (installed when no locator is configured)
// reproduces today's layout: dataDir/repos/<projectID>, ignoring tenantID,
// since self-hosted and open mode have exactly one implicit tenant. A managed
// composition returns a tenant-scoped root, keyed by the immutable project id
// rather than any client-supplied name.
type StorageLocator interface {
	ProjectRoot(tenantID, projectID string) (string, error)
}

// defaultStorageLocator reproduces the pre-seam layout: every project lives at
// dataDir/repos/<projectID>, regardless of tenant.
type defaultStorageLocator struct{ dataDir string }

func (l defaultStorageLocator) ProjectRoot(_ string, projectID string) (string, error) {
	return filepath.Join(l.dataDir, "repos", projectID), nil
}

// IngestAction is the decision an IngestFilter makes about one object upload.
type IngestAction int

const (
	// IngestAccept stores the object exactly as received.
	IngestAccept IngestAction = iota
	// IngestReject refuses the write. The upload fails with 422 and the
	// filter's reason is returned to the caller.
	IngestReject
)

// IngestFilter inspects one object's bytes before the core writes it, so a
// composition can refuse content that must never be stored — the RFC 0004
// secret-scanning gate for public projects is the motivating case. The core
// calls Filter after it has verified the uploaded bytes hash to the URL's
// content address (so a filter never has to re-derive the hash) and before the
// object is written to the store.
//
// The default (installed when no filter is configured) accepts every object,
// matching today's behavior.
type IngestFilter interface {
	Filter(ctx context.Context, principal serverauth.Principal, projectID, hash string, body []byte) (action IngestAction, reason string, err error)
}

type passthroughIngestFilter struct{}

func (passthroughIngestFilter) Filter(context.Context, serverauth.Principal, string, string, []byte) (IngestAction, string, error) {
	return IngestAccept, "", nil
}

// CapabilitiesFunc builds the GET /api/v1/capabilities document for one
// request. It is called with no access-control check, exactly like /healthz:
// the capabilities document contains deployment shape and negotiation
// information, never repository or identity data.
//
// The default (installed when no function is configured, i.e. legacy open
// mode with no composition wired in) returns the open-mode document described
// in RFC 0004: deployment "open", no auth methods, and the base feature set.
// Self-hosted installs its own function returning its existing document. A
// managed composition returns organizations, invitations, domains, and
// service-token features and lists provider start URLs in auth_methods.
type CapabilitiesFunc func(*http.Request) map[string]any

func defaultCapabilities(*http.Request) map[string]any {
	return map[string]any{
		"deployment":         "open",
		"api_version":        "v1",
		"auth_methods":       []string{},
		"bootstrap_required": false,
		"features":           []string{"projects", "history", "skills", "project_ids"},
	}
}

// noopAuditor is the default serverauth.Auditor: it records nothing, matching
// today's behavior for open mode and for any composition that has not yet
// installed one.
type noopAuditor struct{}

func (noopAuditor) Record(context.Context, serverauth.AuditEvent) error { return nil }

// allowLimiter is the default serverauth.Limiter: every request is approved,
// matching today's behavior of enforcing only the fixed max-object-size
// ceiling (which the core applies independently via WithMaxObjectBytes).
type allowLimiter struct{}

func (allowLimiter) Check(context.Context, serverauth.Principal, serverauth.LimitRequest) error {
	return nil
}
