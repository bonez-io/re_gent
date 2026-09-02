package redact

import (
	"strings"
	"testing"
)

func TestHomePaths_OSSpellings(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "macOS",
			content: "/Users/alice/Projects/re_gent/main.go",
			want:    "~/Projects/re_gent/main.go",
		},
		{
			name:    "linux",
			content: "/home/bob/src/app/main.go",
			want:    "~/src/app/main.go",
		},
		{
			name:    "windows",
			content: `C:\Users\carol\Documents\project\file.txt`,
			want:    `~\Documents\project\file.txt`,
		},
		{
			name:    "windows lowercase drive letter",
			content: `d:\Users\dave\file.txt`,
			want:    `~\file.txt`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(HomePaths([]byte(tc.content), nil, nil))
			if got != tc.want {
				t.Fatalf("HomePaths(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

func TestHomePaths_UnrelatedPathsUntouched(t *testing.T) {
	cases := []string{
		"/usr/local/bin/rgt",
		"/opt/homebrew/bin/go",
		"/var/log/syslog",
		"/etc/passwd",
		`C:\Program Files\Go\bin\go.exe`,
	}
	for _, content := range cases {
		got := string(HomePaths([]byte(content), nil, nil))
		if got != content {
			t.Errorf("HomePaths(%q) = %q, want unchanged", content, got)
		}
	}
}

func TestHomePaths_ExplicitHomes(t *testing.T) {
	// Uses a non-standard home root (no "/Users/" or "/home/" substring) so
	// this exercises the explicit homes list rather than the generic OS
	// patterns, and confirms the word-boundary check doesn't over-match a
	// different, longer username sharing the same prefix.
	content := "workspace at /srv/data/homedir/shay/re_gent, see /srv/data/homedir/shayliv/other too"
	got := string(HomePaths([]byte(content), []string{"/srv/data/homedir/shay"}, nil))
	want := "workspace at ~/re_gent, see /srv/data/homedir/shayliv/other too"
	if got != want {
		t.Fatalf("HomePaths = %q, want %q", got, want)
	}
}

func TestHomePaths_Usernames(t *testing.T) {
	content := "Reported by shay via shay@example.com, cc shayliv (different user)"
	got := string(HomePaths([]byte(content), nil, []string{"shay"}))
	want := "Reported by <user> via <user>@example.com, cc shayliv (different user)"
	if got != want {
		t.Fatalf("HomePaths = %q, want %q", got, want)
	}
}

func TestHomePaths_CombinedRealisticToolOutput(t *testing.T) {
	content := "Wrote file to /Users/shay/Projects/re_gent_headless/internal/redact/redact.go for user shay"
	got := string(HomePaths([]byte(content), nil, []string{"shay"}))
	if strings.Contains(got, "/Users/shay") {
		t.Fatalf("home path leaked in output: %q", got)
	}
	if !strings.Contains(got, "~/Projects/re_gent_headless/internal/redact/redact.go") {
		t.Fatalf("expected scrubbed path preserved, got %q", got)
	}
	if !strings.Contains(got, "<user>") {
		t.Fatalf("expected username replaced, got %q", got)
	}
}
