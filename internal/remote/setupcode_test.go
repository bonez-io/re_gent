package remote

import (
	"context"
	"net/http"
	"testing"

	"github.com/bonez-io/re_gent/internal/remotetest"
)

func TestExchangeSetupCodeReturnsCredentialOrgAndServerURL(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableSetupCodes()
	code := srv.MintSetupCode("acme", srv.URL())

	result, err := ExchangeSetupCode(context.Background(), http.DefaultClient, srv.URL(), code, "test-machine")
	if err != nil {
		t.Fatalf("ExchangeSetupCode: %v", err)
	}
	if result.Token == "" {
		t.Error("result has no credential")
	}
	if result.Org != "acme" {
		t.Errorf("Org = %q, want %q", result.Org, "acme")
	}
	if result.ServerURL != srv.URL() {
		t.Errorf("ServerURL = %q, want %q", result.ServerURL, srv.URL())
	}
}

func TestExchangeSetupCodeReusedIsInvalid(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableSetupCodes()
	code := srv.MintSetupCode("acme", srv.URL())

	if _, err := ExchangeSetupCode(context.Background(), http.DefaultClient, srv.URL(), code, "m1"); err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	_, err := ExchangeSetupCode(context.Background(), http.DefaultClient, srv.URL(), code, "m2")
	if err == nil {
		t.Fatal("expected the second exchange of the same code to fail")
	}
	if !IsSetupCodeInvalid(err) {
		t.Errorf("IsSetupCodeInvalid(%v) = false, want true", err)
	}
	if IsSetupCodeExpired(err) {
		t.Errorf("a reused code should not report as expired")
	}
}

func TestExchangeSetupCodeExpired(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableSetupCodes()
	code := srv.MintSetupCode("acme", srv.URL())
	srv.ExpireSetupCode(code)

	_, err := ExchangeSetupCode(context.Background(), http.DefaultClient, srv.URL(), code, "m1")
	if err == nil {
		t.Fatal("expected an expired code to fail")
	}
	if !IsSetupCodeExpired(err) {
		t.Errorf("IsSetupCodeExpired(%v) = false, want true", err)
	}
	if IsSetupCodeInvalid(err) {
		t.Errorf("an expired code should not report as merely invalid")
	}
}

func TestExchangeSetupCodeUnknownCodeIsInvalid(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableSetupCodes()

	_, err := ExchangeSetupCode(context.Background(), http.DefaultClient, srv.URL(), "NEVER-ISSUED", "m1")
	if err == nil || !IsSetupCodeInvalid(err) {
		t.Fatalf("err = %v, want setup_code_invalid", err)
	}
}
