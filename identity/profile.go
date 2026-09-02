package identity

import "context"

// Profile is what a Provider learns about a person from the OAuth provider
// after a successful Exchange. It is the only thing that survives the
// exchange: the provider access token is discarded once the profile has been
// read (see Provider.Exchange).
type Profile struct {
	Provider      string // "github", "google"
	Subject       string // provider user id, stable
	Login         string // GitHub login; empty for Google
	DisplayName   string
	Email         string // verified primary email, or ""
	EmailVerified bool
	Orgs          []string // GitHub organization logins, only when requested
	AvatarURL     string
}

// Config configures one Provider instance.
type Config struct {
	ClientID, ClientSecret string
	BaseURL                string   // GitHub Enterprise Server; "" for github.com
	Scopes                 []string // defaults per provider
	ReadOrgs               bool     // request read:org and fill Profile.Orgs
}

// Provider is one OAuth2 identity provider: GitHub, Google, or a fake used in
// tests and the dev composition.
type Provider interface {
	// Name is the provider's short identifier, e.g. "github" or "google". It
	// is also the path segment Handlers matches routes on and the value
	// stored in State.Provider and Profile.Provider.
	Name() string

	// AuthURL builds the URL the browser is redirected to in order to start
	// the provider's consent screen. state is the opaque, already-signed
	// state token; redirectURL is the callback URL this exchange must later
	// be completed against.
	AuthURL(state, redirectURL string) string

	// Exchange trades an authorization code for a Profile. redirectURL must
	// be identical to the one passed to AuthURL for the same round trip, as
	// providers verify it. The provider access token obtained during the
	// exchange is used only for the profile (and, when configured,
	// organization membership) lookups performed inside Exchange and is
	// never returned or retained afterward.
	Exchange(ctx context.Context, code, redirectURL string) (Profile, error)
}
