// Package serverauth defines the authentication and authorization boundary used
// by both the public self-hosted server and the private managed composition.
package serverauth

import (
	"context"
	"errors"
	"net/http"
)

// ErrUnauthenticated means the request did not carry valid credentials.
var ErrUnauthenticated = errors.New("unauthenticated")

// ErrForbidden means the principal is authenticated but lacks permission.
var ErrForbidden = errors.New("forbidden")

// ErrNotFound denies access without revealing whether a tenant-scoped resource
// exists. Managed policies should prefer it for cross-tenant identifiers.
var ErrNotFound = errors.New("not found")

// Action is one stable operation understood by an access controller.
type Action string

const (
	ActionRequest          Action = "request"
	ActionRepositoriesList Action = "repositories:list"
	ActionRepositoryCreate Action = "repository:create"
	ActionRepositoryRead   Action = "repository:read"
	ActionObjectRead       Action = "object:read"
	ActionObjectWrite      Action = "object:write"
	ActionRefRead          Action = "ref:read"
	ActionRefWrite         Action = "ref:write"
	ActionHistoryRead      Action = "history:read"
	ActionHistoryWrite     Action = "history:write"
	ActionSkillList        Action = "skill:list"
	ActionSkillRead        Action = "skill:read"
)

// Resource identifies the object a permission applies to. RepositoryID is
// empty for global operations. Name is a ref, object hash, skill name, or raw
// route suffix, depending on Kind.
type Resource struct {
	Kind         string
	RepositoryID string
	Name         string
}

// Permission is the complete policy input derived from a request route.
type Permission struct {
	Action   Action
	Resource Resource
}

// Principal is an authenticated identity. TenantID is optional for local
// self-hosting and required by the managed composition.
type Principal struct {
	Subject    string
	TenantID   string
	Roles      []string
	AuthMethod string
}

// Controller authenticates one HTTP request, then makes explicit policy
// decisions. Authenticate must not read or mutate the request body.
//
// Implementations must return ErrUnauthenticated for missing or invalid
// credentials, ErrForbidden for an ordinary denied policy decision, and
// ErrNotFound when resource existence must be concealed. Other errors are
// treated as internal failures and are never exposed to the caller.
type Controller interface {
	Authenticate(*http.Request) (Principal, error)
	Authorize(context.Context, Principal, Permission) error
}

type principalContextKey struct{}

// WithPrincipal records the authenticated principal for downstream handlers.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext returns the authenticated principal attached by the
// server. The boolean is false when the server is running in legacy open mode.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}
