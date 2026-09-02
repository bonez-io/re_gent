package publicgate

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strings"
)

// ErrNotGitRepo is the sentinel wrapped into the error NewPathAllowlist
// returns when root is not inside a git working tree. Use errors.Is to
// check for it; the returned allowlist is still usable in that case (it
// just permits nothing), so a caller that only logs the error and keeps
// going still fails closed rather than open.
var ErrNotGitRepo = errors.New("publicgate: not a git repository")

// PathAllowlist is the set of paths a public project is allowed to
// capture: exactly the paths git currently tracks in the repository,
// minus a fixed denylist of sensitive paths/patterns that are excluded
// even if tracked (see isBlockedPath).
type PathAllowlist struct {
	tracked map[string]bool
}

// NewPathAllowlist builds an allowlist from `git ls-files -z` run in root,
// mirroring how internal/capture shells out to git (see
// internal/capture/author.go's gitConfigValue) rather than depending on a
// git library.
//
// If root is not a git working tree (or git isn't on PATH), it returns a
// non-nil, empty PathAllowlist — Allowed reports false for every path —
// along with an error wrapping ErrNotGitRepo that the caller can surface
// to the user or log.
func NewPathAllowlist(root string) (*PathAllowlist, error) {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return &PathAllowlist{tracked: map[string]bool{}},
			fmt.Errorf("%w (%s): %s", ErrNotGitRepo, root, msg)
	}

	tracked := make(map[string]bool)
	for _, p := range strings.Split(stdout.String(), "\x00") {
		if p == "" {
			continue
		}
		if norm, ok := normalizeRelPath(p); ok {
			tracked[norm] = true
		}
	}
	return &PathAllowlist{tracked: tracked}, nil
}

// Allowed reports whether relPath may be captured for a public project:
// it must normalize to a clean, relative, non-parent-escaping path, must
// not fall under a denylisted path/pattern (.git/, .regent/, .env*,
// *.pem, *.key, id_rsa* — checked before consulting the tracked set, so
// these are excluded even if git tracks them), and must be a path git
// currently tracks.
//
// A nil *PathAllowlist (e.g. the zero value, or NewPathAllowlist's result
// on a non-git root) allows nothing.
func (a *PathAllowlist) Allowed(relPath string) bool {
	norm, ok := normalizeRelPath(relPath)
	if !ok {
		return false
	}
	if isBlockedPath(norm) {
		return false
	}
	if a == nil {
		return false
	}
	return a.tracked[norm]
}

// normalizeRelPath cleans relPath into the forward-slash, repo-relative
// form used as the allowlist's key, rejecting anything that isn't
// straightforwardly relative: absolute paths (POSIX "/...", Windows
// "C:\..." or UNC "\\host\..."), and paths that climb above the root via
// "..".
func normalizeRelPath(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "/") {
		return "", false
	}
	if len(p) >= 2 && p[1] == ':' { // "C:", "d:", ...
		return "", false
	}

	clean := path.Clean(p)
	if clean == "." {
		return "", false
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", false
	}
	return clean, true
}

// isBlockedPath reports whether clean (already normalized by
// normalizeRelPath) falls under a path or pattern that must never be
// captured for a public project, independent of whether git tracks it:
// re_gent's and git's own metadata directories, dotenv files, and private
// key material.
func isBlockedPath(clean string) bool {
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return true
	}
	if clean == ".regent" || strings.HasPrefix(clean, ".regent/") {
		return true
	}

	base := path.Base(clean)
	if strings.HasPrefix(base, ".env") {
		return true
	}
	if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
		return true
	}
	if strings.HasPrefix(base, "id_rsa") {
		return true
	}
	return false
}
