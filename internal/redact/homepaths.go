package redact

import "regexp"

// osHomeDirRe matches the well-known OS home-directory path forms:
// macOS/Darwin "/Users/<name>", Linux "/home/<name>", and Windows
// "C:\Users\<name>" (any drive letter, case-insensitive). Each is replaced
// wholesale with "~", leaving the remainder of the path (and hence the
// user's directory structure) intact — only the identifying directory
// segment is scrubbed.
var osHomeDirRes = []*regexp.Regexp{
	regexp.MustCompile(`/Users/[A-Za-z0-9._-]+`),
	regexp.MustCompile(`/home/[A-Za-z0-9._-]+`),
	regexp.MustCompile(`(?i)[A-Za-z]:\\Users\\[A-Za-z0-9._-]+`),
}

// HomePaths returns a copy of content with:
//   - any "/Users/<name>", "/home/<name>", or "C:\Users\<name>" home
//     directory reference replaced with "~" (the trailing path, e.g.
//     "/Projects/foo", is preserved unchanged);
//   - every path in homes (e.g. a non-standard home directory root the
//     caller already knows about, such as "/mnt/home/shay" or a home dir
//     under a symlinked volume) replaced with "~" wherever it appears as a
//     whole path segment;
//   - every name in usernames replaced with "<user>" wherever it appears
//     as a whole word (word-boundary matched, so "shay" inside "shayliv"
//     or "/usr/share" is left untouched, but "shay" in "alice, shay,
//     bob" or "shay@example.com" is replaced).
//
// Matching uses only RE2-supported constructs (word boundaries, no
// lookaround/backreferences), so it is linear in len(content) regardless
// of the number of homes/usernames supplied.
func HomePaths(content []byte, homes []string, usernames []string) []byte {
	out := content

	for _, re := range osHomeDirRes {
		out = re.ReplaceAll(out, []byte("~"))
	}

	for _, home := range homes {
		if home == "" {
			continue
		}
		re, err := regexp.Compile(regexp.QuoteMeta(home) + `\b`)
		if err != nil {
			continue
		}
		out = re.ReplaceAll(out, []byte("~"))
	}

	for _, user := range usernames {
		if user == "" {
			continue
		}
		re, err := regexp.Compile(`\b` + regexp.QuoteMeta(user) + `\b`)
		if err != nil {
			continue
		}
		out = re.ReplaceAll(out, []byte("<user>"))
	}

	return out
}
