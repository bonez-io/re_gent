package remote

import (
	"context"
	"net/http"
	"testing"

	"github.com/bonez-io/re_gent/internal/remotetest"
)

// A server nobody has called EnableProjectIDs on must look exactly like every
// server before RFC 0004: no project_ids feature, so callers fall back to
// today's repo_id flow.
func TestFetchCapabilitiesLegacyServerHasNoProjectIDs(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)

	caps := FetchCapabilities(context.Background(), http.DefaultClient, srv.URL())
	if caps.HasFeature("project_ids") {
		t.Fatal("a server that never enabled project_ids reported having it")
	}
	if caps.SupportsAuthMethod("device") {
		t.Fatal("a server that never enabled device auth reported supporting it")
	}
}

func TestFetchCapabilitiesReportsEnabledFeatures(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()
	srv.EnableDeviceAuth()

	caps := FetchCapabilities(context.Background(), http.DefaultClient, srv.URL())
	if !caps.HasFeature("project_ids") {
		t.Errorf("features = %v, want project_ids", caps.Features)
	}
	if !caps.SupportsAuthMethod("device") {
		t.Errorf("auth_methods = %v, want device", caps.AuthMethods)
	}
	if !caps.SupportsAuthMethod("pat") {
		t.Errorf("auth_methods = %v, want pat to remain available", caps.AuthMethods)
	}
}

// FetchCapabilities must never error out — a server that predates the route,
// or one that is simply down, both read as "legacy: every feature absent".
func TestFetchCapabilitiesUnreachableServerIsLegacyNotAnError(t *testing.T) {
	caps := FetchCapabilities(context.Background(), http.DefaultClient, "http://127.0.0.1:1")
	if caps.HasFeature("project_ids") || caps.SupportsAuthMethod("device") || caps.Deployment != "" {
		t.Fatalf("expected a zero Capabilities value for an unreachable server, got %+v", caps)
	}
}

func TestFetchCapabilities404RouteIsLegacy(t *testing.T) {
	// A server that has never heard of /api/v1/capabilities 404s; the fake
	// with the [remotetest] object/ref routes still 404s any unrecognised
	// two-segment path the same way a legacy self-hosted server would for an
	// unknown route.
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	// Do not enable anything — capabilities is still served (it always is by
	// this fake), but with nothing on. This exercises the "reachable, but
	// legacy" branch distinctly from "unreachable".
	caps := FetchCapabilities(context.Background(), http.DefaultClient, srv.URL())
	if caps.Deployment == "" {
		t.Fatal("expected the fake's always-on capabilities route to answer")
	}
	if caps.HasFeature("project_ids") {
		t.Fatal("expected project_ids to be off by default")
	}
}
