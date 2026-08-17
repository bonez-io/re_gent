# Changelog

All notable changes to re_gent will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `rgt repair blame` recomputes the stored blame maps for history already on
  disk. Blame is annotated at write time, so a fix to the line diff reaches
  only steps recorded after it; the maps already written keep whatever the old
  diff believed, and until now the only remedy was deleting `.regent/` and the
  history with it. Repair walks each session ref from its root, rewrites every
  (step, file) map with the current diff, and reports how many it rewrote and
  how many were already correct. It touches no canonical object, is idempotent
  (a second run rewrites nothing), and is safe to interrupt.
- `rgt pull` fetches a connected project's history from the server into the
  machine-local cache. With no arguments it asks the server which sessions
  exist, so a teammate who has just cloned — and has therefore pushed nothing
  and can name nothing — gets the team's history with one command. Afterwards
  `rgt log`, `show`, `blame` and `sessions` read it locally, with no network.
  A session whose local history is not contained in the server's is reported
  and left alone rather than overwritten; the other sessions are still pulled.

### Changed
- A connected project whose cache is empty now reports that it is connected and
  not yet pulled, and names `rgt pull`. `rgt log`, `sessions` and `status` used
  to answer "no sessions" and send the user to `rgt doctor` for a wiring
  problem they did not have, while the server held every session.
- A project is identified by the repository it belongs to, not by the folder it
  sits in. Identity comes from the normalised git remote, so the same
  repository cloned over https or ssh is one project, two unrelated checkouts
  both called `api` are two, and renaming your folder changes nothing. A
  repository with no remote falls back to its root commit; a directory that is
  not a repository keeps its folder name. Identity is frozen into the project
  binding once connected and is never re-derived underneath a project.
- `rgt connect --as <name>` registers a project under a name you choose. It is
  recorded in the binding, so it is given once rather than repeated.
- The machine-local server-mode cache is keyed by server as well as project.
  One cache was shared by every server a project had been pointed at, blending
  their histories and crossing their upload watermarks. **Existing caches will
  not be found at the new path**; they are disposable by design, but anything
  spooled and not yet uploaded under the old path is not migrated.

### Fixed
- `rgt blame` named the line after the one that changed. The line diff paired
  two encoders that do not decode each other's output, so every chunk came
  back holding a neighbouring line. `rgt show` diffs are computed at query
  time and were corrected by the rebuild. Blame is annotated at write time, so
  maps recorded before the fix stayed wrong on disk — run `rgt repair blame`
  to rewrite them.
- re_gent identified itself by filename, so a binary called anything other
  than `rgt` or `regent` wired hooks to a bare `rgt` that PATH resolved
  elsewhere or nowhere, `rgt doctor` reported "nothing will be captured" over
  a project that was capturing normally, and `rgt init` stopped removing its
  own previous hooks on re-run. Hooks are now recognised by the subcommand
  they run, and the installer embeds any binary path that is not a temporary
  Go build.
- Connecting a project you had already been using locally now connects it.
  "Connected" was decided by whether `.regent/config.toml` existed, but
  `rgt init` writes that file unconditionally — so every locally-used project
  already looked connected. `rgt connect` took its disconnect branch: it
  reported "is not connected to a server", changed nothing and exited
  non-zero; where it got further, it removed the agent hooks and reported
  success. One predicate now answers the question, and it requires both a
  server address and a project identity.
- Re-pointing a project at a different server registers it there, keeps the
  hooks, and names the previous server so it is clear that history stayed
  behind. It is a move, not a disconnect.
- `rgt connect` no longer trusts local config blindly. If the server has no
  record of the project, it re-registers instead of printing "already
  connected" and leaving every future upload to be rejected in silence.
- Connecting twice is safe: the picker's "selecting a connected project
  disconnects it" applies to a tick in the picker, not to `rgt connect` typed
  inside a project, which is an instruction and never removes hooks.
- Connecting a project keeps the history recorded before it. Binding a project
  to a server moves every read to a machine-local cache, and everything
  captured beforehand stayed in the project's own `.regent/` where no command
  read it, no command uploaded it and nothing said so — `rgt log --session
  <id>` exited 1 for a session that had worked a minute earlier. `rgt connect`
  now copies that history into the cache, uploads it, and reports how many
  sessions and steps came across. Anything it cannot carry — a session the
  cache already holds, an upload the server refused — is named on screen and
  never folded into the success line. Projects already connected before this
  change are not migrated; their history is still in `.regent/`.

### Removed
- The interactive project picker, and every path that reached it. Typing bare
  `rgt` — the first thing anyone does with an unfamiliar CLI — opened a
  full-screen multi-select over the filesystem, listing the projects it found
  below the current directory. Marking one that was already connected meant
  *disconnect*, so a working project was one space keypress away from losing
  its wiring and its hooks, in a screen reachable by accident. Bare `rgt` now
  prints help and changes nothing; `rgt connect` run outside a project says to
  `cd` into the one you want and run it there, rather than searching; and
  `rgt disconnect` is the only way to disconnect a project.
- `rgt login` and `rgt whoami`. The server performs no authentication, so a
  sign-in command and an identity command implied a security model that did not
  exist. Identity comes from `git config user.name` / `user.email`.
- `rgt setup`, folded into `rgt connect`. The two were one job with two
  implementations that disagreed: on whether backing out of the picker was an
  error, on whether selecting an already-connected project disconnected it, and
  on whether the server URL was remembered.

  All three names still respond — hidden, failing, and naming what to run
  instead — because "unknown command" tells a script nothing.

### Changed
- `rgt connect` now takes the server URL optionally, remembers it after a
  successful run, offers to commit the wiring for teammates, and treats
  selecting a connected project as "disconnect it" — everything `setup` did.
- `rgt cat` is hidden from `rgt --help`. It is a debugging tool; it remains
  runnable by name.
- A `401` from the server no longer advises running `rgt login`, which would
  have produced "unknown command".

## [1.0.0] - 2026-05-14

### Added
- OpenCode integration with interactive agent selection during `rgt init`
- Idempotent `rgt init` with existing hook detection (safe to re-run)

### Changed
- README rewritten for stable release
- Cleaned up CLI output and error messages

### Fixed
- Linting errors in init command
- GoReleaser config pointing to incorrect repository name

## [0.2.0] - 2026-05-10

### Added
- Codex CLI capture parity (full hook integration with `SessionStart`, `UserPromptSubmit`, `PostToolUse`, `Stop`)
- Enhanced `rgt log` with full conversation display, graph view, and improved UX
- Workspace snapshotting and blame computation in message hook
- VSCode extension section in README
- Comprehensive unit test suite for internal packages
- Discord community link
- Roadmap and issue templates for phases

### Fixed
- Restored PostToolUse hook with blame computation
- Homebrew tap push using dedicated token
- Codex parity review blockers

## [0.1.2] - 2026-05-04

### Added
- Homebrew installation support (`brew tap regent-vcs/tap && brew install regent`)

### Fixed
- GoReleaser action upgraded to v6 for config v2 support

## [0.1.1-beta] - 2026-05-02

### Added
- Initial beta release
- Core object store implementation (blobs, trees, steps)
- Content-addressed storage with BLAKE3 hashing
- SQLite-based index for fast queries
- CLI commands: `init`, `log`, `status`, `cat`, `version`
- Claude Code integration via PostToolUse hook
- Session tracking with DAG-based step lineage
- Automatic workspace snapshotting
- Transcript chain for conversation history
- Basic blame tracking

---

## Links

- [1.0.0](https://github.com/regent-vcs/regent/compare/v0.2.0...v1.0.0)
- [0.2.0](https://github.com/regent-vcs/regent/compare/v0.1.2...v0.2.0)
- [0.1.2](https://github.com/regent-vcs/regent/compare/v0.1.1-beta...v0.1.2)
- [0.1.1-beta](https://github.com/regent-vcs/regent/releases/tag/v0.1.1-beta)
