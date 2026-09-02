package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/bonez-io/re_gent/serverauth"
)

// isMutationAction reports whether an action writes something, and therefore
// gets an audit event on every outcome (not just a denial).
func isMutationAction(action serverauth.Action) bool {
	switch action {
	case serverauth.ActionRepositoryCreate, serverauth.ActionRepositoryWrite,
		serverauth.ActionObjectWrite, serverauth.ActionRefWrite, serverauth.ActionHistoryWrite:
		return true
	default:
		return false
	}
}

// denialOutcome classifies an Authorize error into the AuditEvent.Outcome it
// produces: a policy decision is "denied", anything else (an internal
// controller failure) is "error".
func denialOutcome(err error) string {
	switch {
	case errors.Is(err, serverauth.ErrUnauthenticated), errors.Is(err, serverauth.ErrForbidden), errors.Is(err, serverauth.ErrNotFound):
		return "denied"
	default:
		return "error"
	}
}

// auditTarget derives TargetType/TargetID from a classified resource.
func auditTarget(resource serverauth.Resource) (targetType, targetID string) {
	targetType = resource.Kind
	switch {
	case resource.RepositoryID != "" && resource.Name != "" && resource.Name != resource.RepositoryID:
		targetID = resource.RepositoryID + ":" + resource.Name
	case resource.RepositoryID != "":
		targetID = resource.RepositoryID
	default:
		targetID = resource.Name
	}
	return targetType, targetID
}

// audit builds and records one AuditEvent for a classified permission and
// outcome. Recording is best-effort: a failure to record is logged, not
// surfaced to the caller, so an audit sink outage never blocks a live agent
// turn.
func (s *Server) audit(ctx context.Context, principal serverauth.Principal, permission serverauth.Permission, outcome string) {
	tenantID := permission.Resource.TenantID
	if tenantID == "" {
		tenantID = principal.TenantID
	}
	targetType, targetID := auditTarget(permission.Resource)
	event := serverauth.AuditEvent{
		RequestID:  requestIDFromContext(ctx),
		Actor:      principal.Subject,
		ActorKind:  principal.AuthMethod,
		TenantID:   tenantID,
		ProjectID:  permission.Resource.RepositoryID,
		Action:     permission.Action,
		TargetType: targetType,
		TargetID:   targetID,
		Outcome:    outcome,
		At:         time.Now().UTC(),
	}
	if err := s.auditor.Record(ctx, event); err != nil {
		s.logf("record audit event action=%s outcome=%s: %v", event.Action, event.Outcome, err)
	}
}

// statusRecorder captures the status code a downstream handler writes, so the
// core can tell after the fact whether a mutation it authorized actually
// succeeded, without threading the permission into every handler that can
// produce one.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}
