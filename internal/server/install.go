package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

// The install endpoints are the "one paste" onboarding path: a teammate runs
//
//	curl -fsSL http://<team-server>/install | sh
//
// and gets `rgt` with no Go toolchain required. Both routes are intentionally
// UNAUTHENTICATED (matched before the auth gate in ServeHTTP, like /healthz):
// they expose the open-source binary and a bootstrap script, never repo data.

// installScriptTemplate is the POSIX sh installer served by GET /install. It is
// personalized with the base URL of THIS server so the script downloads the
// binary from the same host it was fetched from. It embeds no secret and is
// safe to serve publicly.
//
// {{.BaseURL}} is the scheme+host this request arrived on (e.g.
// http://team-server:7654), with no trailing slash.
var installScriptTemplate = template.Must(template.New("install").Parse(`#!/bin/sh
# re_gent one-line installer (server-hosted; no Go toolchain required).
#
#   curl -fsSL {{.BaseURL}}/install | sh
#
# It installs the ` + "`rgt`" + ` binary and verifies it runs. It writes no config
# (the repo's committed .regent/config.toml handles server wiring) and never
# embeds a token.
set -eu

BASE_URL="{{.BaseURL}}"

info() { printf '  %s\n' "$*"; }
warn() { printf '  ! %s\n' "$*" >&2; }

printf '\n== re_gent installer ==\n\n'

# ---------------------------------------------------------------------------
# 0. Idempotent: if a working rgt is already on PATH, skip the install.
# ---------------------------------------------------------------------------
if command -v rgt >/dev/null 2>&1 && rgt version >/dev/null 2>&1; then
  info "rgt already installed: $(command -v rgt)"
else
  # -------------------------------------------------------------------------
  # 1. Pick an install dir: prefer /usr/local/bin when writable, else
  #    ~/.local/bin (created if needed and added to PATH for this run).
  # -------------------------------------------------------------------------
  BIN_DIR=""
  if [ -w /usr/local/bin ] 2>/dev/null; then
    BIN_DIR="/usr/local/bin"
  else
    BIN_DIR="$HOME/.local/bin"
    mkdir -p "$BIN_DIR"
    case ":${PATH}:" in
      *":${BIN_DIR}:"*) : ;;
      *) PATH="${BIN_DIR}:${PATH}"; export PATH ;;
    esac
  fi

  TARGET="$BIN_DIR/rgt"
  installed=0

  # -------------------------------------------------------------------------
  # 2. Primary path: download the prebuilt binary matching THIS machine's
  #    OS/arch. We detect it with uname and ask the server for the right build;
  #    the server serves a per-platform binary when it has one, else its own.
  # -------------------------------------------------------------------------
  OS="$(uname -s 2>/dev/null || echo unknown)"
  ARCH="$(uname -m 2>/dev/null || echo unknown)"
  case "$OS" in
    Darwin) GOOS=darwin ;;
    Linux) GOOS=linux ;;
    MINGW*|MSYS*|CYGWIN*|Windows_NT) GOOS=windows ;;
    *) GOOS="" ;;
  esac
  case "$ARCH" in
    x86_64|amd64) GOARCH=amd64 ;;
    arm64|aarch64) GOARCH=arm64 ;;
    *) GOARCH="" ;;
  esac
  BIN_URL="${BASE_URL}/bin/rgt"
  if [ -n "$GOOS" ] && [ -n "$GOARCH" ]; then
    BIN_URL="${BASE_URL}/bin/rgt?os=${GOOS}&arch=${GOARCH}"
  fi

  info "Downloading rgt (${GOOS:-?}/${GOARCH:-?}) from ${BASE_URL}/bin/rgt ..."
  if curl -fsSL "$BIN_URL" -o "$TARGET.tmp"; then
    chmod +x "$TARGET.tmp"
    # Verify the downloaded binary actually executes on this OS/arch before
    # committing it; a mismatch fails here and falls through to the fallback.
    if "$TARGET.tmp" version >/dev/null 2>&1 || "$TARGET.tmp" --help >/dev/null 2>&1; then
      mv "$TARGET.tmp" "$TARGET"
      installed=1
      info "Installed rgt to $TARGET"
    else
      warn "Downloaded binary does not run here (likely an OS/arch mismatch)."
      rm -f "$TARGET.tmp"
    fi
  else
    warn "Could not download ${BASE_URL}/bin/rgt."
    rm -f "$TARGET.tmp" 2>/dev/null || true
  fi

  # -------------------------------------------------------------------------
  # 3. Fallback: build from source with 'go install' when Go is present.
  # -------------------------------------------------------------------------
  if [ "$installed" -ne 1 ]; then
    if command -v go >/dev/null 2>&1; then
      info "Falling back to 'go install' from source ..."
      go install github.com/regent-vcs/regent/cmd/rgt@latest
      GOBIN_DIR=$(go env GOBIN 2>/dev/null || true)
      [ -n "$GOBIN_DIR" ] || GOBIN_DIR="$(go env GOPATH 2>/dev/null || echo "$HOME/go")/bin"
      case ":${PATH}:" in
        *":${GOBIN_DIR}:"*) : ;;
        *) PATH="${GOBIN_DIR}:${PATH}"; export PATH ;;
      esac
    else
      warn "The prebuilt binary did not run here and Go is not installed."
      warn "Install Go (https://go.dev/dl/) and re-run, or ask your team for a"
      warn "prebuilt rgt binary matching your OS/arch and place it on your PATH."
      exit 1
    fi
  fi
fi

# ---------------------------------------------------------------------------
# 4. Verify rgt is reachable and runs.
# ---------------------------------------------------------------------------
if ! command -v rgt >/dev/null 2>&1; then
  warn "rgt was installed but is not on your PATH. Open a new shell or add its"
  warn "directory to PATH, then re-run this installer."
  exit 1
fi
info "rgt is ready: $(command -v rgt)"
rgt version 2>/dev/null || true

# ---------------------------------------------------------------------------
# 5. Wire the current project, so this one command is the whole setup.
# ---------------------------------------------------------------------------
# Only when we are standing in a project: "curl | sh" inherits whatever
# directory the teammate happened to be in, and connecting blindly would
# scatter .regent/ into home directories. A .git entry is the marker (it is a
# file, not a directory, inside worktrees and submodules). The server is open,
# so no token is involved.
# Hand over to the interactive picker so choosing projects is part of the same
# command. Redirecting from /dev/tty is what makes this possible at all: under
# "curl | sh" stdin is the script being read, so the wizard would otherwise get
# EOF instead of keystrokes. /dev/tty still refers to the real terminal.
if [ -r /dev/tty ]; then
  rgt setup "{{.BaseURL}}" < /dev/tty || {
    warn "Setup did not finish. You can re-run it any time with:"
    warn "  rgt setup {{.BaseURL}}"
  }
else
  # No terminal at all (CI, a provisioning script): install only, and say what
  # to run rather than guessing which project was meant.
  printf '\n== rgt installed ==\n\n'
  info "No terminal available, so no project was wired. Run this yourself:"
  info ""
  info "  rgt setup {{.BaseURL}}"
  info ""
fi
printf '\n'
`))

// installData is the template context for installScriptTemplate.
type installData struct {
	BaseURL string
}

// baseURL derives the scheme+host this request arrived on, with no trailing
// slash, so a served script targets the same server the teammate fetched it
// from. The scheme honors X-Forwarded-Proto (set by a TLS-terminating proxy)
// and otherwise defaults to http; the host is the request's Host header.
func baseURL(r *http.Request) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		// A forwarding proxy may send a comma-separated list; the first entry is
		// the scheme the client actually used.
		if i := strings.IndexByte(proto, ','); i >= 0 {
			proto = proto[:i]
		}
		scheme = strings.TrimSpace(proto)
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// handleInstallScript serves the personalized POSIX sh installer. It is
// unauthenticated and embeds no secret.
func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	data := installData{BaseURL: baseURL(r)}
	if err := installScriptTemplate.Execute(w, data); err != nil {
		s.logf("render install script: %v", err)
		// The header is already written; nothing more can be sent safely.
	}
}

// binaryTargets is the allow-list of client platforms GET /bin/rgt can serve a
// prebuilt binary for, mapping each {GOOS, GOARCH} to its on-disk filename in
// the binaries dir. Filenames come ONLY from this table, never from request
// input, so a client-supplied os/arch can never escape the binaries dir.
var binaryTargets = map[[2]string]string{
	{"darwin", "amd64"}:  "rgt_darwin_amd64",
	{"darwin", "arm64"}:  "rgt_darwin_arm64",
	{"linux", "amd64"}:   "rgt_linux_amd64",
	{"linux", "arm64"}:   "rgt_linux_arm64",
	{"windows", "amd64"}: "rgt_windows_amd64.exe",
}

// downloadFilename is the name the client saves the binary as (Windows keeps
// its .exe suffix; every other platform gets plain "rgt").
func downloadFilename(goos string) string {
	if goos == "windows" {
		return "rgt.exe"
	}
	return "rgt"
}

// handleBinary serves a runnable rgt binary for the requesting teammate's
// platform, taken from the ?os=&arch= query (filled by the install script's
// `uname` detection) and defaulting to the server's own platform when absent.
// Resolution order: (1) a prebuilt binary from the binaries dir matching the
// request, (2) the server's own executable if the request matches the
// server's platform, (3) a 404 so the installer falls back to source.
//
// Unauthenticated like /install; the only filenames ever opened come from the
// binaryTargets allow-list, so a client-supplied os/arch can never escape the
// binaries dir.
func (s *Server) handleBinary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}

	goos := r.URL.Query().Get("os")
	goarch := r.URL.Query().Get("arch")
	if goos == "" && goarch == "" {
		goos, goarch = runtime.GOOS, runtime.GOARCH
	}
	filename, ok := binaryTargets[[2]string{goos, goarch}]
	if !ok {
		httpError(w, http.StatusBadRequest, fmt.Sprintf("unsupported os/arch %q/%q", goos, goarch))
		return
	}

	// 1. Prefer a prebuilt binary for the requested platform.
	if s.binariesDir != "" {
		path := filepath.Join(s.binariesDir, filename)
		if f, err := os.Open(path); err == nil {
			defer f.Close()
			if fi, ferr := f.Stat(); ferr == nil && !fi.IsDir() {
				s.serveBinaryFile(w, r, f, fi, downloadFilename(goos))
				return
			}
		}
	}

	// 2. Fall back to the server's own executable, but only for a request that
	//    matches the server's platform — handing a Linux binary to a macOS
	//    teammate is worse than a clean 404 that triggers the source fallback.
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		exe, err := os.Executable()
		if err != nil {
			s.logf("locate own executable: %v", err)
			httpError(w, http.StatusInternalServerError, "server binary unavailable")
			return
		}
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		f, err := os.Open(exe)
		if err != nil {
			s.logf("open own executable %q: %v", exe, err)
			httpError(w, http.StatusInternalServerError, "server binary unavailable")
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			s.logf("stat own executable %q: %v", exe, err)
			httpError(w, http.StatusInternalServerError, "server binary unavailable")
			return
		}
		s.serveBinaryFile(w, r, f, fi, downloadFilename(goos))
		return
	}

	// 3. No prebuilt binary for a cross-platform request.
	httpError(w, http.StatusNotFound, fmt.Sprintf("no prebuilt rgt for %s/%s; the installer will build from source", goos, goarch))
}

// serveBinaryFile streams an open binary with download headers. http.ServeContent
// sets Content-Length and handles Range/HEAD correctly rather than buffering.
func (s *Server) serveBinaryFile(w http.ResponseWriter, r *http.Request, f *os.File, fi os.FileInfo, name string) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
	http.ServeContent(w, r, name, fi.ModTime(), f)
}
