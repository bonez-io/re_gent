package remote

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/bonez-io/re_gent/internal/remotetest"
)

func TestDeviceLoginPendingThenApproved(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableDeviceAuth()
	ctx := context.Background()

	auth, err := StartDeviceAuthorization(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("StartDeviceAuthorization: %v", err)
	}
	if auth.DeviceCode == "" || auth.UserCode == "" || auth.VerificationURL == "" {
		t.Fatalf("incomplete device authorization: %+v", auth)
	}

	_, err = PollDeviceToken(ctx, http.DefaultClient, srv.URL(), auth.DeviceCode)
	var pending *DevicePollError
	if !errors.As(err, &pending) || pending.Code != "authorization_pending" {
		t.Fatalf("poll before approval: err = %v, want authorization_pending", err)
	}

	srv.ApproveDevice(auth.DeviceCode, "access-tok", "refresh-tok", 3600)

	pair, err := PollDeviceToken(ctx, http.DefaultClient, srv.URL(), auth.DeviceCode)
	if err != nil {
		t.Fatalf("poll after approval: %v", err)
	}
	if pair.AccessToken != "access-tok" || pair.RefreshToken != "refresh-tok" || pair.ExpiresIn != 3600 {
		t.Errorf("token pair = %+v", pair)
	}
}

func TestDeviceLoginDenied(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableDeviceAuth()
	ctx := context.Background()

	auth, err := StartDeviceAuthorization(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("StartDeviceAuthorization: %v", err)
	}
	srv.DenyDevice(auth.DeviceCode)

	_, err = PollDeviceToken(ctx, http.DefaultClient, srv.URL(), auth.DeviceCode)
	var denied *DevicePollError
	if !errors.As(err, &denied) || denied.Code != "denied" {
		t.Fatalf("err = %v, want denied", err)
	}
}

func TestRefreshTokensExchangesRefreshTokenForNewPair(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.SetRefreshResult("refresh-1", remotetest.RefreshResult{
		AccessToken: "access-2", RefreshToken: "refresh-2", ExpiresIn: 3600,
	})

	pair, err := RefreshTokens(context.Background(), http.DefaultClient, srv.URL(), "refresh-1")
	if err != nil {
		t.Fatalf("RefreshTokens: %v", err)
	}
	if pair.AccessToken != "access-2" || pair.RefreshToken != "refresh-2" {
		t.Errorf("pair = %+v", pair)
	}
}

func TestRefreshTokensRejectsUnknownRefreshToken(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)

	if _, err := RefreshTokens(context.Background(), http.DefaultClient, srv.URL(), "never-issued"); err == nil {
		t.Fatal("expected an error refreshing an unknown token")
	}
}
