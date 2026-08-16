# Changelog

All notable changes to re_gent will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Removed
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
