package capture

import (
	"os"
	"os/exec"
	"strings"

	"github.com/bonez-io/re_gent/internal/store"
)

// ResolveAuthor returns the best available Author for the current environment.
// Lookup order (first non-empty wins):
//  1. REGENT_AUTHOR_NAME / REGENT_AUTHOR_EMAIL environment variables
//  2. GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL environment variables
//  3. git config user.name / user.email (subprocess — may be slow first call)
//
// If nothing is found, an empty Author is returned; callers treat it as
// "unknown" and continue normally.
func ResolveAuthor() store.Author {
	if name := os.Getenv("REGENT_AUTHOR_NAME"); name != "" {
		return store.Author{
			Name:  name,
			Email: os.Getenv("REGENT_AUTHOR_EMAIL"),
		}
	}
	if name := os.Getenv("GIT_AUTHOR_NAME"); name != "" {
		return store.Author{
			Name:  name,
			Email: os.Getenv("GIT_AUTHOR_EMAIL"),
		}
	}
	return resolveFromGitConfig()
}

func resolveFromGitConfig() store.Author {
	name := gitConfigValue("user.name")
	email := gitConfigValue("user.email")
	return store.Author{Name: name, Email: email}
}

func gitConfigValue(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
