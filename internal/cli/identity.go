package cli

import (
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"strings"

	"lukechampine.com/blake3"
)

// A project's identity is the repository it belongs to.
//
// It used to be the folder it sat in, which is wrong in both directions at
// once: two people whose checkout is called "api" registered the same id and
// shared one history on the server, and renaming your own folder derived a
// different id, leaving your history on the server with nothing pointing at it.
//
// The remote is the name everyone involved already agrees on, so it is the
// identity. A repository with no remote falls back to its first commit, which
// is the one fact about a repository that never changes. A directory that is
// not a repository at all has nothing better than its name, and keeps it.

// maxRepoID is the server's limit, mirrored from remote.ValidateRepoID.
const maxRepoID = 64

// disambiguatorLen is how much hash is appended when the readable form had to
// be altered. Eight hex characters is 32 bits: enough that two projects
// colliding is not something anyone will see, short enough to leave the
// readable part readable.
const disambiguatorLen = 8

// deriveRepoID produces the server-legal id for a project, most specific
// source first.
func deriveRepoID(projectRoot string) string {
	if id := identityFromRemote(gitRemoteURL(projectRoot)); id != "" {
		return id
	}
	if commit := firstCommit(projectRoot); commit != "" {
		// Deliberately no folder name in here. Including it would read better
		// and would reintroduce exactly the bug this replaces: rename the
		// folder, get a different identity. Anyone who wants a readable name
		// for such a project sets one explicitly.
		return "repo-" + commit[:12]
	}
	return identityFromFolder(projectRoot)
}

// identityFromRemote turns a git remote URL into a server-legal project id, or
// "" if the URL is not one it can make sense of.
func identityFromRemote(remote string) string {
	host, path := splitRemote(remote)
	if host == "" || path == "" {
		return ""
	}

	segments := append([]string{host}, strings.Split(path, "/")...)
	cleaned := make([]string, 0, len(segments))
	lossy := false
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		safe, changed := reduceToLegal(seg)
		lossy = lossy || changed
		cleaned = append(cleaned, safe)
	}
	if len(cleaned) < 2 {
		return ""
	}

	// The "/" separators become "-" as a matter of structure, not of
	// sanitising, so joining is not what makes an id lossy.
	return finalise(strings.Join(cleaned, "-"), remote, lossy)
}

// identityFromFolder is the old behaviour, kept for a directory that is not a
// git repository at all. There is nothing better available there, and the
// collisions it allows are the reason everything above it exists.
func identityFromFolder(projectRoot string) string {
	safe, lossy := reduceToLegal(strings.ToLower(filepath.Base(projectRoot)))
	if id := finalise(safe, projectRoot, lossy); id != "" {
		return id
	}
	return "repo"
}

// finalise applies the rules the server enforces, and the one rule it does not:
// an id that had to be altered to become legal carries a hash of what it was
// derived from.
//
// That last part is the difference between a readable id and a correct one.
// Truncating two long remotes that share a prefix, or flattening two names that
// differ only outside the legal character set, produces one id for two
// projects — and the failure is invisible, because both simply start writing
// into the same history.
func finalise(cleaned, source string, lossy bool) string {
	cleaned = strings.TrimLeft(cleaned, "._-")
	if cleaned == "" {
		return ""
	}

	if lossy || len(cleaned) > maxRepoID {
		limit := maxRepoID - disambiguatorLen - 1
		if len(cleaned) > limit {
			cleaned = cleaned[:limit]
		}
		cleaned = strings.TrimRight(cleaned, "._-")
		if cleaned == "" {
			cleaned = "repo"
		}
		return cleaned + "-" + identityDisambiguator(source)
	}

	if reservedRepoIDs[cleaned] {
		return cleaned + "-repo"
	}
	return cleaned
}

// reduceToLegal maps one path segment onto the characters the server accepts,
// reporting whether anything had to be replaced.
func reduceToLegal(segment string) (string, bool) {
	var b strings.Builder
	changed := false
	for _, r := range segment {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
			changed = true
		}
	}
	return b.String(), changed
}

func identityDisambiguator(source string) string {
	sum := blake3.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])[:disambiguatorLen]
}

// splitRemote pulls the host and repository path out of a git remote, in any of
// the spellings git accepts for the same repository. Returning them separately
// keeps the caller from having to know which spelling it was given.
func splitRemote(remote string) (host, path string) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", ""
	}

	// scp-style: git@github.com:owner/repo.git — not a URL, and the most
	// common form on a developer's machine.
	if !strings.Contains(remote, "://") {
		at := strings.LastIndex(remote, "@")
		colon := strings.Index(remote[at+1:], ":")
		if colon < 0 {
			return "", ""
		}
		host = remote[at+1 : at+1+colon]
		path = remote[at+1+colon+1:]
	} else {
		rest := remote[strings.Index(remote, "://")+3:]
		// Credentials must not survive into an id: it is sent to a server and
		// written into a config file people are told to commit.
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return "", ""
		}
		host, path = rest[:slash], rest[slash+1:]
	}

	// The port is dropped. The same repository reached over a different port is
	// one project; keeping the port would split its history in two, which is
	// the failure this whole change exists to remove.
	if colon := strings.Index(host, ":"); colon >= 0 {
		host = host[:colon]
	}

	host = strings.ToLower(host)
	path = strings.ToLower(strings.Trim(path, "/"))
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	return host, path
}

// gitRemoteURL returns the project's origin remote, or "" when there is none.
func gitRemoteURL(projectRoot string) string {
	return gitOutput(projectRoot, "config", "--get", "remote.origin.url")
}

// firstCommit returns the root commit of the project's history.
//
// A repository can have more than one root — an unrelated history merged in,
// say. The smallest hash is chosen so that the answer does not depend on the
// order git happens to list them, since an identity that changes between runs
// is worse than no identity at all.
func firstCommit(projectRoot string) string {
	out := gitOutput(projectRoot, "rev-list", "--max-parents=0", "HEAD")
	if out == "" {
		return ""
	}
	roots := strings.Fields(out)
	smallest := roots[0]
	for _, r := range roots[1:] {
		if r < smallest {
			smallest = r
		}
	}
	return smallest
}

func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Fingerprint is the stable identity of a source repository (RFC 0004,
// "Project enrollment: connect once"): the git remote's normalized host and
// path, plus the repository's smallest root commit, hashed together. It is
// what the client sends the project-enrollment API so that connecting the
// same repository twice — from two clones, from https and from ssh — lands
// on the same project instead of creating a duplicate.
//
// This is deliberately a different identity than deriveRepoID above.
// deriveRepoID produces a server-legal *string* by sanitising and truncating,
// because the legacy protocol interpolates it directly into a URL path
// segment. A Fingerprint is never interpolated anywhere; it is one opaque
// field in a JSON body, so it carries the readable Remote and RootCommit
// alongside the hash rather than destroying them to fit a charset.
type Fingerprint struct {
	// Remote is "host/path", normalized exactly as splitRemote does: lower
	// case, credentials stripped, port stripped, ".git" stripped. Empty when
	// the repository has no remote.
	Remote string
	// RootCommit is the repository's smallest root commit (see firstCommit).
	// Can be empty for a git repository with no commits yet.
	RootCommit string
	// Hex is blake3(Remote + "\n" + RootCommit), hex-encoded. A repository
	// with no remote hashes "\n"+RootCommit, so the empty Remote is still
	// part of what was hashed rather than silently absent from it.
	Hex string
}

// sourceFingerprint computes projectRoot's Fingerprint. ok is false only when
// projectRoot is not a git repository at all — the one case RFC 0004 says has
// no fingerprint, and where the CLI must fall back to requiring --as and
// always creating a new project. A git repository with no remote still gets a
// fingerprint (from its root commit alone); a git repository with no commits
// yet still gets a fingerprint (from its remote alone, or an empty one if it
// has neither).
func sourceFingerprint(projectRoot string) (fp Fingerprint, ok bool) {
	if !isGitRepo(projectRoot) {
		return Fingerprint{}, false
	}
	host, path := splitRemote(gitRemoteURL(projectRoot))
	remote := ""
	if host != "" && path != "" {
		remote = host + "/" + path
	}
	root := firstCommit(projectRoot)

	sum := blake3.Sum256([]byte(remote + "\n" + root))
	return Fingerprint{
		Remote:     remote,
		RootCommit: root,
		Hex:        hex.EncodeToString(sum[:]),
	}, true
}

// isGitRepo reports whether dir is inside a git working tree. It is checked
// separately from gitRemoteURL/firstCommit because both of those legitimately
// return "" for a real repository (no remote configured, no commits yet) —
// only this distinguishes "no such fact" from "not a git repository at all".
func isGitRepo(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}
