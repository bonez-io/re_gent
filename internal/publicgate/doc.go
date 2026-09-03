// Package publicgate implements the ingestion policy for public re_gent
// projects: which paths are allowed to leave the workspace at all, and
// what happens to a blob's content when secrets or operator-identifying
// home paths are found in it. It composes internal/redact's detectors
// with a git-tracked-paths allowlist into one accept/reject/rewrite
// decision per blob.
//
// This is docs/rfcs/0004's "Privacy gate for public capture", host- and
// transport-independent. Two call sites are expected to use it:
//
//   - Client-side, in internal/capture, right before a blob (tool input,
//     tool result, message, or file snapshot content) is spooled or
//     uploaded to a public project. Rejecting here means the object is
//     never written and the finding is reported to the pusher only.
//   - Server-side, in server.IngestFilter, as defense in depth against a
//     client that skipped the gate (old client, bypassed hook, direct API
//     call): the same Gate runs again before an object is durably stored.
//
// publicgate does not talk to the object store, the server, or git
// directly beyond shelling out to read the tracked-file list for
// PathAllowlist; it has no knowledge of re_gent's Step/Tree/Blob types.
// Wiring a Gate's Decision into capture's write path and into
// server.IngestFilter belongs to whoever owns those packages.
package publicgate
