// Package identity implements the GitHub and Google OAuth providers shared
// by the self-hosted and managed re_gent compositions (RFC 0005 Appendix B).
// It is deliberately small and has no database and no cookie of its own: a
// Provider turns an authorization code into a Profile, a Signer turns a
// State into an opaque token and back, and Handlers wires the two together
// behind an http.Handler that a composition mounts under its own auth
// prefix. Everything that decides who gets in — sessions, invitations,
// organizations — is the composition's Resolver.
//
// # Wiring
//
// A composition builds one Handlers per server, listing every provider it
// has configured:
//
//	signer := identity.NewHMACSigner(stateKey, 10*time.Minute)
//	providers := map[string]identity.Provider{
//		"github": identity.NewGitHub(identity.Config{
//			ClientID:     githubClientID,
//			ClientSecret: githubClientSecret,
//			ReadOrgs:     true, // needed to check "allowed GitHub organizations"
//		}),
//		"google": identity.NewGoogle(identity.Config{
//			ClientID:     googleClientID,
//			ClientSecret: googleClientSecret,
//		}),
//	}
//	auth := identity.Handlers(providers, signer, orgResolver{store: identities},
//		identity.WithSessionNonce(func(r *http.Request) string {
//			return sessionNonceFromCookie(r) // "" for an anonymous visitor is fine
//		}),
//		identity.WithCallbackBaseURL("https://team.example.com/api/v1/auth"),
//		identity.WithLogger(identity.LoggerFunc(func(event string, fields map[string]any) {
//			log.Printf("identity: %s %v", event, fields)
//		})),
//	)
//	mux.Handle("/api/v1/auth/", http.StripPrefix("/api/v1/auth", auth))
//
// orgResolver implements identity.Resolver against the composition's own
// user store, applying whatever admission rules it owns (invitation match,
// existing identity, allowed organizations, open registration, ...) and
// returning identity.Outcome{UserID: ..., Redirect: ...} on success or
// identity.Outcome{Refused: true, Reason: "not_invited"} otherwise. Handlers
// takes care of the two routes ("GET .../{provider}/start" and "GET
// .../{provider}/callback"), signing and verifying State, the open-redirect
// check on Return, and turning a Refused Outcome or a provider-side failure
// into the right redirect; orgResolver only has to decide who is let in.
//
// # Testing without a real OAuth app
//
// identity.NewFake builds a Provider whose Exchange looks the authorization
// code up directly in a map (code == map key), so Handlers and a Resolver
// can be tested, or a dev environment run, without any provider
// configuration at all. To exercise the real GitHub provider end to end —
// including GitHub Enterprise Server's "/api/v3" and
// "/login/oauth/access_token" paths — identity.NewFakeServer starts an
// httptest.Server standing in for github.com and identity.NewGitHub can be
// pointed at it through Config.BaseURL.
package identity
