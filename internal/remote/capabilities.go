package remote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// maxCapabilitiesResponseBytes bounds the capabilities document. It is a
// small, fixed-shape JSON object; there is no legitimate reason for it to be
// large, and an unbounded read here would let a misbehaving or malicious
// server stall or exhaust a client that runs this on every connect.
const maxCapabilitiesResponseBytes = 16 << 10

// Capabilities is the server's self-description (RFC 0004), discovered
// through the one public, unauthenticated route every deployment exposes.
type Capabilities struct {
	Deployment        string   `json:"deployment"`
	APIVersion        string   `json:"api_version"`
	AuthMethods       []string `json:"auth_methods"`
	BootstrapRequired bool     `json:"bootstrap_required"`
	Features          []string `json:"features"`
}

// HasFeature reports whether name appears in the server's feature list, for
// example "project_ids".
func (c Capabilities) HasFeature(name string) bool {
	for _, f := range c.Features {
		if f == name {
			return true
		}
	}
	return false
}

// SupportsAuthMethod reports whether name appears in the server's
// auth_methods list, for example "device" or "pat".
func (c Capabilities) SupportsAuthMethod(name string) bool {
	for _, m := range c.AuthMethods {
		if m == name {
			return true
		}
	}
	return false
}

// FetchCapabilities discovers what a server supports.
//
// It deliberately never returns an error: a self-hosted server that predates
// capabilities, one that is simply unreachable at this moment, and one that
// answers with something that is not valid JSON are all, from a caller's
// point of view, the same fact — "this server's capabilities are unknown" —
// and the zero Capabilities value already means exactly that (every feature
// and auth method absent, deployment "", so HasFeature and
// SupportsAuthMethod correctly answer false for anything). Every caller in
// this codebase treats "capabilities absent" as "legacy: behave exactly as
// before capabilities existed", which is what makes this safe to call
// unconditionally at the top of `rgt connect` and `rgt auth login` without
// regressing self-hosted servers that have never heard of this route.
//
// A caller that needs to distinguish "server is definitely offline" from
// "server is definitely legacy" already has /healthz for that; this call does
// not draw that line, because a legacy server's capabilities route also
// 404s, and there would be no way to tell the two apart from here anyway.
func FetchCapabilities(ctx context.Context, client *http.Client, serverURL string) Capabilities {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(serverURL, "/")+"/api/v1/capabilities", nil)
	if err != nil {
		return Capabilities{}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return Capabilities{}
	}
	defer closeBody(resp)
	if resp.StatusCode != http.StatusOK {
		return Capabilities{}
	}

	var caps Capabilities
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxCapabilitiesResponseBytes)).Decode(&caps); err != nil {
		return Capabilities{}
	}
	return caps
}
