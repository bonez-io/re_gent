package server

import (
	"net/http"
	"os"
	"path/filepath"
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
# It installs the ` + "`rgt`" + ` binary and verifies it runs{{if .AuthRequired}}, then prints the one
# remaining manual step: exporting the shared team token{{end}}. It writes no config
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
  # 2. Primary path: download the prebuilt binary this server is running.
  #    Works with zero external dependencies when the teammate is on the same
  #    OS/arch as the server (the common case: a Linux server + Linux
  #    devcontainers, or a macOS team + macOS host).
  # -------------------------------------------------------------------------
  info "Downloading rgt from ${BASE_URL}/bin/rgt ..."
  if curl -fsSL "${BASE_URL}/bin/rgt" -o "$TARGET.tmp"; then
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
# 5. You're wired up. This server is {{if .AuthRequired}}token-protected{{else}}open (no auth){{end}}.
# ---------------------------------------------------------------------------
{{if .AuthRequired}}# This server requires a shared team token (a secret, never committed). The
# CLIENT env var is REGENT_TOKEN (the server side uses REGENT_SERVER_TOKEN — do
# not confuse the two).
printf '\n== One step left ==\n\n'
if [ -n "${REGENT_TOKEN:-}" ]; then
  info "REGENT_TOKEN is already set — you're done. Run an agent turn in the repo."
else
  info "Export the shared team token, then start working in the repo:"
  info ""
  info "  export REGENT_TOKEN=<your-team-token>"
  info ""
  info "Add that line to your shell profile (~/.zshrc, ~/.bashrc) to persist it."
fi
info ""
info "If you are inside a repo that already commits .regent/config.toml, the"
info "server wiring (url + repo_id) is already handled — nothing else to do."
info "Otherwise, wire this machine to the server yourself:"
info ""
info "  rgt connect {{.BaseURL}} --token \$REGENT_TOKEN"
info ""
{{else}}# This server is open (no auth) — no token, no secrets. Fine on a private
# network/VPN. Nothing left to export.
printf '\n== You are done ==\n\n'
info "If you are inside a repo that already commits .regent/config.toml, the"
info "server wiring (url + repo_id) is already handled — just run an agent turn."
info "Otherwise, wire this machine to the server yourself (no token needed):"
info ""
info "  rgt connect {{.BaseURL}}"
info ""
{{end}}printf '\n'
`))

// installData is the template context for installScriptTemplate.
type installData struct {
	BaseURL string
	// AuthRequired is true when THIS server was started with an auth token, in
	// which case the served script tells the teammate to export REGENT_TOKEN.
	// When false the server is open (no auth) and the script mentions no token.
	AuthRequired bool
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
	data := installData{BaseURL: baseURL(r), AuthRequired: s.authToken != ""}
	if err := installScriptTemplate.Execute(w, data); err != nil {
		s.logf("render install script: %v", err)
		// The header is already written; nothing more can be sent safely.
	}
}

// handleBinary serves the server's OWN running executable so a teammate on the
// same OS/arch gets a dependency-free binary. It is unauthenticated and serves
// exactly one fixed path (os.Executable()), never a client-supplied path, so
// there is no traversal surface.
func (s *Server) handleBinary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		s.logf("locate own executable: %v", err)
		httpError(w, http.StatusInternalServerError, "server binary unavailable")
		return
	}
	// Resolve symlinks so we serve the real file (and can stat/open it).
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

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="rgt"`)
	// http.ServeContent sets Content-Length and handles Range/HEAD correctly,
	// streaming the file rather than buffering it.
	http.ServeContent(w, r, "rgt", fi.ModTime(), f)
}
