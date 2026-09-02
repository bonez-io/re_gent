package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type contextKey int

const (
	resourceTenantContextKey contextKey = iota
	requestIDContextKey
)

// withResourceTenant records the classified request's resource TenantID
// (serverauth.Permission.Resource.TenantID) for handlers deep in the call tree
// — openRepoTenant in particular — that need it but do not receive the
// Permission directly.
func withResourceTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, resourceTenantContextKey, tenantID)
}

func resourceTenantFromContext(ctx context.Context) string {
	tenantID, _ := ctx.Value(resourceTenantContextKey).(string)
	return tenantID
}

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey, id)
}

func requestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey).(string)
	return id
}

// newRequestID generates the X-Request-ID the core echoes on every response
// when the client did not supply one itself.
func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is effectively unheard of; fall back to a
		// timestamp so a request id is still produced.
		return fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	return "req_" + hex.EncodeToString(b)
}
