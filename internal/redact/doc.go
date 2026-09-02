// Package redact detects and masks secrets and user-identifying
// home-directory paths in arbitrary byte content — tool inputs, tool
// results, chat messages, and file snapshots — captured by re_gent.
//
// It knows nothing about re_gent's capture pipeline, storage format, or
// server, and imports nothing outside the Go standard library. Two call
// sites are expected to use it, per docs/rfcs/0004 "Privacy gate for public
// capture":
//
//   - Client-side, in internal/capture, before a blob is spooled or
//     uploaded to a public project: run Detect/Redact on tool payloads and
//     messages, and HomePaths on anything that might carry the operator's
//     home directory or OS username.
//   - Server-side, in server.IngestFilter, as defense in depth: the same
//     detectors run again before an object is durably stored, so a client
//     that skips (or is tricked into skipping) the gate does not leak a
//     secret into a public project.
//
// redact only classifies and rewrites bytes; it does not decide accept/
// reject policy, and it does not know about file paths, git, or
// allowlists — that policy layer lives in internal/publicgate, which
// composes redact's detectors with a path allowlist to produce a single
// accept/reject/rewrite decision per blob.
package redact
