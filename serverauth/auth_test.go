package serverauth

import (
	"errors"
	"testing"
)

func TestAnonymousPrincipalShape(t *testing.T) {
	p := Anonymous()
	if p.Subject != "" {
		t.Fatalf("Subject = %q, want empty", p.Subject)
	}
	if p.AuthMethod != "anonymous" {
		t.Fatalf("AuthMethod = %q, want anonymous", p.AuthMethod)
	}
}

func TestErrNoCredentialsWrapsErrUnauthenticated(t *testing.T) {
	if !errors.Is(ErrNoCredentials, ErrUnauthenticated) {
		t.Fatal("ErrNoCredentials does not satisfy errors.Is(_, ErrUnauthenticated); every existing 401 check must keep classifying it as an authentication failure")
	}
	wrapped := errors.New("wrap: " + ErrNoCredentials.Error())
	_ = wrapped // sanity: Error() does not panic
}

func TestErrQuotaExceededMessage(t *testing.T) {
	err := &ErrQuotaExceeded{Reason: "over the object limit"}
	if got := err.Error(); got != "quota exceeded: over the object limit" {
		t.Fatalf("Error() = %q", got)
	}
	if (&ErrQuotaExceeded{}).Error() != "quota exceeded" {
		t.Fatalf("Error() with empty reason = %q, want %q", (&ErrQuotaExceeded{}).Error(), "quota exceeded")
	}
	var target *ErrQuotaExceeded
	if !errors.As(error(err), &target) {
		t.Fatal("errors.As failed to unwrap *ErrQuotaExceeded")
	}
}
