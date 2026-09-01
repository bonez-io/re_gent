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
