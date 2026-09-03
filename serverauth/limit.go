package serverauth

import (
	"context"
	"fmt"
)

// LimitKind names the kind of write a Limiter is being asked to approve.
type LimitKind string

const (
	// LimitKindObject is an object (blob) upload. Bytes is the object size.
	LimitKindObject LimitKind = "object"
	// LimitKindRef is a ref move (CAS update). Bytes is always 0.
	LimitKindRef LimitKind = "ref"
	// LimitKindProject is a project/repository creation. Bytes is always 0.
	LimitKindProject LimitKind = "project"
)

// LimitRequest describes one write the core is about to perform, so a Limiter
// can approve or refuse it before the write happens.
type LimitRequest struct {
	Kind      LimitKind
	TenantID  string
	ProjectID string
	// Bytes is the size of the object being written for LimitKindObject, and
	// 0 for every other kind.
	Bytes int64
}

// ErrQuotaExceeded is returned by Limiter.Check to refuse a write. The public
// server core maps it to HTTP 413 with a JSON body carrying the stable error
// code "quota_exceeded" and Reason as the message, so a CLI can recognize it
// programmatically rather than by parsing prose.
type ErrQuotaExceeded struct {
	Reason string
}

func (e *ErrQuotaExceeded) Error() string {
	if e.Reason == "" {
		return "quota exceeded"
	}
	return fmt.Sprintf("quota exceeded: %s", e.Reason)
}

// Limiter approves or refuses a write before it happens. The public server
// core consults it before writing an object, before moving a ref, and before
// creating a project. The default (used when no Limiter is installed) allows
// every request, matching today's behavior of enforcing only the fixed
// max-object-size ceiling.
//
// Check should return *ErrQuotaExceeded to refuse the write with a reason a
// client can display. Any other error is treated as an internal failure (the
// request fails with 500 and the detail stays server-side, exactly like a
// Controller.Authorize failure).
type Limiter interface {
	Check(ctx context.Context, principal Principal, request LimitRequest) error
}
