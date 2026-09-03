package identity

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestNewGoogleDefaultScopes(t *testing.T) {
	p := NewGoogle(Config{ClientID: "id", ClientSecret: "secret"}).(*googleProvider)
	if got := strings.Join(p.cfg.Scopes, " "); got != "openid email profile" {
		t.Errorf("default scopes = %q", got)
	}
}

func TestGoogleAuthURL(t *testing.T) {
	p := NewGoogle(Config{ClientID: "abc123"})
	raw := p.AuthURL("signed-state", "https://team.example.com/api/v1/auth/google/callback")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("AuthURL produced an unparseable URL: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "abc123" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q", q.Get("response_type"))
	}
	if q.Get("state") != "signed-state" {
		t.Errorf("state = %q", q.Get("state"))
	}
	if q.Get("redirect_uri") != "https://team.example.com/api/v1/auth/google/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
}

func TestParseGoogleUserInfo(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    Profile
		wantErr bool
	}{
		{
			name:    "boolean email_verified",
			payload: `{"sub":"12345","email":"person@example.com","email_verified":true,"name":"A Person","picture":"https://example.com/a.png"}`,
			want: Profile{
				Provider:      "google",
				Subject:       "12345",
				DisplayName:   "A Person",
				Email:         "person@example.com",
				EmailVerified: true,
				AvatarURL:     "https://example.com/a.png",
			},
		},
		{
			name:    "string email_verified",
			payload: `{"sub":"999","email":"other@example.com","email_verified":"true","name":"Other"}`,
			want: Profile{
				Provider:      "google",
				Subject:       "999",
				DisplayName:   "Other",
				Email:         "other@example.com",
				EmailVerified: true,
			},
		},
		{
			name:    "unverified email",
			payload: `{"sub":"1","email":"unverified@example.com","email_verified":false}`,
			want: Profile{
				Provider:      "google",
				Subject:       "1",
				Email:         "unverified@example.com",
				EmailVerified: false,
			},
		},
		{
			name:    "missing sub is rejected",
			payload: `{"email":"nosub@example.com","email_verified":true}`,
			wantErr: true,
		},
		{
			name:    "malformed json",
			payload: `not json`,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseGoogleUserInfo([]byte(c.payload))
			if c.wantErr {
				if err == nil {
					t.Fatal("parseGoogleUserInfo succeeded, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGoogleUserInfo: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parseGoogleUserInfo() = %+v, want %+v", got, c.want)
			}
		})
	}
}
