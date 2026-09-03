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

// ErrNoCredentials is a specific, distinguishable form of ErrUnauthenticated: it
// means the request carried no credentials at all (no Authorization header, no
// session cookie), as opposed to credentials that were present but invalid,
// expired, or malformed.
//
// Controller.Authenticate implementations should return this exact error (or
// wrap it) for the "nothing was presented" case. The public server core treats
// it specially: instead of failing the request immediately, it builds an
// Anonymous principal and still calls Authorize, so a composition's policy can
// choose to grant a public read (see the "public project" case in RFC 0004).
// Every other Authenticate error, including a plain ErrUnauthenticated for bad
// credentials, still fails the request with 401 before Authorize is ever
// called.
//
// ErrNoCredentials wraps ErrUnauthenticated, so existing errors.Is(err,
// ErrUnauthenticated) checks (including the default self-hosted and open-mode
// policies) keep classifying it as an authentication failure without any
// change on their part.
var ErrNoCredentials = errNoCredentials{}

type errNoCredentials struct{}

func (errNoCredentials) Error() string { return "no credentials presented" }
func (errNoCredentials) Unwrap() error { return ErrUnauthenticated }

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
	// ActionRepositoryWrite covers mutating a project's own record (for
	// example renaming it via PATCH), as distinct from ActionHistoryWrite,
	// which covers mutating the captured history it contains.
	ActionRepositoryWrite Action = "repository:write"
	ActionObjectRead      Action = "object:read"
	ActionObjectWrite     Action = "object:write"
	ActionRefRead         Action = "ref:read"
	ActionRefWrite        Action = "ref:write"
	ActionHistoryRead     Action = "history:read"
	ActionHistoryWrite    Action = "history:write"
	ActionSkillList       Action = "skill:list"
	ActionSkillRead       Action = "skill:read"
	ActionIdentityRead    Action = "identity:read"
	ActionTokenRead       Action = "token:read"
	ActionTokenWrite      Action = "token:write"
	ActionUserList        Action = "user:list"
	ActionUserCreate      Action = "user:create"
	ActionMemberRead      Action = "member:read"
	ActionMemberWrite     Action = "member:write"
)

// Resource identifies the object a permission applies to. RepositoryID is
// empty for global operations. Name is a ref, object hash, skill name, or raw
// route suffix, depending on Kind.
//
// TenantID is the resource's owning tenant/organization, filled in by route
// classification when it is derivable: from an "/o/{org}/…" or
// "/api/v1/orgs/{org}/…" URL prefix when the route has one, and otherwise from
// the authenticated principal's own TenantID (a request is always scoped to
// its caller's tenant when the URL itself does not name one). It is always
// empty for legacy/self-hosted routes and for open mode, where there is
// exactly one implicit tenant and every principal's TenantID is "".
type Resource struct {
	Kind         string
	RepositoryID string
	TenantID     string
	Name         string
}

// Permission is the complete policy input derived from a request route.
//
// Ref is populated for ref routes ("/{project}/refs/{ref...}") with the same
// full ref name as Resource.Name, so a policy can restrict ref:write by ref
// name without having to know that ref routes are the only ones that set
// Resource.Name to a ref. This is the plumbing for a "contributor" role that
// may only push session refs it owns (see RFC 0004); no policy in this
// repository implements that restriction — the field only carries the ref name
// through to whatever Controller.Authorize a composition supplies.
type Permission struct {
	Action   Action
	Resource Resource
	Ref      string
}

// Principal is an authenticated identity. TenantID is optional for local
// self-hosting and required by the managed composition.
type Principal struct {
	Subject    string
	TenantID   string
	Roles      []string
	AuthMethod string
}

// Anonymous returns the principal the public server core attaches to a request
// that carried no credentials (Controller.Authenticate returned an error
// wrapping ErrNoCredentials). Subject is always "" and AuthMethod is always
// "anonymous", so a policy can recognize it without a separate flag.
//
// Every default policy in this repository (the self-hosted identity store, and
// the open-mode no-controller path, which never even constructs a principal)
// denies the anonymous principal exactly as it denies a request with bad
// credentials. A composition may write a policy that grants read actions to
// Anonymous() for a resource it has marked public — that is the sole
// documented exception to deny-by-default in RFC 0003.
func Anonymous() Principal {
	return Principal{AuthMethod: "anonymous"}
}

// Controller authenticates one HTTP request, then makes explicit policy
// decisions. Authenticate must not read or mutate the request body.
//
// Implementations must return ErrNoCredentials (or an error wrapping it) when
// the request carries no credentials at all, ErrUnauthenticated for credentials
// that are present but invalid, ErrForbidden for an ordinary denied policy
// decision, and ErrNotFound when resource existence must be concealed. Other
// errors are treated as internal failures and are never exposed to the caller.
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
