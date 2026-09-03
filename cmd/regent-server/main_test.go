package main

import "testing"

func TestValidateUnauthenticatedBind(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		insecure bool
		wantErr  bool
	}{
		{name: "IPv4 loopback", addr: "127.0.0.1:7654"},
		{name: "IPv6 loopback", addr: "[::1]:7654"},
		{name: "localhost", addr: "localhost:7654"},
		{name: "all IPv4 denied", addr: "0.0.0.0:7654", wantErr: true},
		{name: "empty host denied", addr: ":7654", wantErr: true},
		{name: "all IPv6 denied", addr: "[::]:7654", wantErr: true},
		{name: "explicit insecure override", addr: "0.0.0.0:7654", insecure: true},
		{name: "malformed", addr: "7654", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateUnauthenticatedBind(test.addr, test.insecure)
			if test.wantErr && err == nil {
				t.Fatalf("validateUnauthenticatedBind(%q) = nil, want error", test.addr)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validateUnauthenticatedBind(%q) = %v, want nil", test.addr, err)
			}
		})
	}
}

func TestResolveAuthMode(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		insecure bool
		want     string
		wantErr  bool
	}{
		{name: "secure by default", mode: "auto", want: "self-hosted"},
		{name: "legacy override selects open", mode: "auto", insecure: true, want: "open"},
		{name: "explicit loopback open", mode: "open", want: "open"},
		{name: "explicit secure", mode: "self-hosted", want: "self-hosted"},
		{name: "conflicting flags", mode: "self-hosted", insecure: true, wantErr: true},
		{name: "unknown", mode: "magic", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveAuthMode(test.mode, test.insecure)
			if test.wantErr {
				if err == nil {
					t.Fatalf("resolveAuthMode(%q, %v) = %q, want error", test.mode, test.insecure, got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("resolveAuthMode(%q, %v) = %q, %v; want %q, nil", test.mode, test.insecure, got, err, test.want)
			}
		})
	}
}

func TestRecoverOwnerRefusesAnUninitializedDirectory(t *testing.T) {
	if err := recoverOwner(t.TempDir()); err == nil {
		t.Fatal("recoverOwner succeeded without an identity database")
	}
}
