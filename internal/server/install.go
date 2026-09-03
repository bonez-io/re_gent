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
//
// The script deliberately does NOT inspect the terminal before wiring. It used
// to: it ran the picker with stdin redirected from the tty when one looked
// available, and otherwise printed instructions and wired nothing. Two things
// were wrong with that.
//
// The guard was unreliable. A `[ -r /dev/tty ]` test succeeds whenever the
// device node is readable, but the redirect then fails with "Device not
// configured" if the process has no controlling terminal. The failure landed in
// the fallback branch, which warned and continued, so the installer exited 0
// having wired nothing. This was observed in the test suite itself.
//
// And the degraded path was the main path. Team onboarding happens in
// devcontainers, over SSH, in CI and in provisioning scripts — none of which
// have a terminal — so "no terminal" could not be the case that gives up.
//
// Wiring is now unconditional and `rgt connect` decides. There is nothing left
// to redirect a terminal for: the picker it fed is gone (#28), and connect
// either wires the project the script is standing in or names the fix.
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
VERBOSE="${REGENT_VERBOSE:-0}"

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  PURPLE='\033[38;5;141m'; GREEN='\033[38;5;42m'; AMBER='\033[38;5;214m'
  BLUE='\033[38;5;69m'; BOLD='\033[1m'; DIM='\033[2m'; RESET='\033[0m'
else
  PURPLE=''; GREEN=''; AMBER=''; BLUE=''; BOLD=''; DIM=''; RESET=''
fi

detail() { [ "$VERBOSE" = 1 ] || [ "$VERBOSE" = true ] || return 0; printf '  %s%s%s\n' "$DIM" "$*" "$RESET"; }
step() { printf '  %s│%s  %s✓%s  %s\n' "$PURPLE" "$RESET" "$GREEN" "$RESET" "$*"; }
warn() { printf '  %s│%s  %s!%s  %s\n' "$PURPLE" "$RESET" "$AMBER" "$RESET" "$*" >&2; }

# Animate only when the output is a terminal. Pipes and CI get one stable line
# per result, with no cursor movement or escape-code debris.
spin_wait() {
  spin_pid="$1"; spin_label="$2"; spin_i=0
  if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    while kill -0 "$spin_pid" 2>/dev/null; do
      case "$spin_i" in
        0) spin_frame='⣾' ;; 1) spin_frame='⣽' ;; 2) spin_frame='⣻' ;; 3) spin_frame='⢿' ;;
        4) spin_frame='⡿' ;; 5) spin_frame='⣟' ;; 6) spin_frame='⣯' ;; *) spin_frame='⣷' ;;
      esac
      printf '\r  %s│%s  %s%s%s  %s%s%s' "$PURPLE" "$RESET" "$PURPLE" "$spin_frame" "$RESET" "$DIM" "$spin_label" "$RESET"
      spin_i=$(( (spin_i + 1) % 8 ))
      sleep 0.08
    done
    printf '\r\033[2K'
  fi
  if wait "$spin_pid"; then return 0; else return $?; fi
}

printf '\n'
printf '%s╭──────────────────────────────────────────────────────╮%s\n' "$PURPLE" "$RESET"
printf '%s│%s  %s◆  RE_GENT%s  %sAGENT VERSION CONTROL%s                 %s│%s\n' "$PURPLE" "$RESET" "$PURPLE$BOLD" "$RESET" "$DIM" "$RESET" "$PURPLE" "$RESET"
printf '%s│%s  %s INSTALL %s  %sOne-command project setup%s              %s│%s\n' "$PURPLE" "$RESET" "$BLUE$BOLD" "$RESET" "$BOLD" "$RESET" "$PURPLE" "$RESET"
printf '%s╰──────────────────────────────────────────────────────╯%s\n\n' "$PURPLE" "$RESET"

# ---------------------------------------------------------------------------
# 0. Always (re)install from THIS server rather than keeping whatever rgt is
#    already on PATH. Skipping when one existed meant anyone who had installed
#    before silently kept their old binary and never received a fix: re-running
#    the installer appeared to work and changed nothing. Every version here
#    reports "dev", so there is nothing meaningful to compare — downloading a
#    few MB is the cheap, correct answer.
# ---------------------------------------------------------------------------
if command -v rgt >/dev/null 2>&1; then
  detail "Replacing existing rgt: $(command -v rgt)"
fi
  # -------------------------------------------------------------------------
  # 1. Pick an install dir without touching a binary outside this shell's PATH.
  #    Tests and provisioning can set REGENT_INSTALL_DIR for full isolation.
  # -------------------------------------------------------------------------
  BIN_DIR="${REGENT_INSTALL_DIR:-}"
  if [ -z "$BIN_DIR" ] && command -v rgt >/dev/null 2>&1; then
    existing_dir=$(dirname "$(command -v rgt)")
    if [ -w "$existing_dir" ]; then
      BIN_DIR="$existing_dir"
    fi
  fi
  if [ -z "$BIN_DIR" ]; then
    case ":${PATH}:" in
      *":/usr/local/bin:"*)
        if [ -w /usr/local/bin ] 2>/dev/null; then BIN_DIR=/usr/local/bin; fi
        ;;
    esac
  fi
  if [ -z "$BIN_DIR" ]; then
    BIN_DIR="$HOME/.local/bin"
  fi
  mkdir -p "$BIN_DIR"
  PATH="${BIN_DIR}:${PATH}"
  export PATH

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

  detail "Downloading rgt (${GOOS:-?}/${GOARCH:-?}) from ${BASE_URL}/bin/rgt"
  curl -fsSL "$BIN_URL" -o "$TARGET.tmp" &
  download_pid=$!
  if spin_wait "$download_pid" "Downloading re_gent CLI"; then
    chmod +x "$TARGET.tmp"
    # Verify the downloaded binary actually executes on this OS/arch before
    # committing it; a mismatch fails with a platform-specific explanation.
    if "$TARGET.tmp" version >/dev/null 2>&1 || "$TARGET.tmp" --help >/dev/null 2>&1; then
      mv "$TARGET.tmp" "$TARGET"
      installed=1
      detail "Installed rgt to $TARGET"
    else
      warn "Downloaded binary does not run here (likely an OS/arch mismatch)."
      rm -f "$TARGET.tmp"
    fi
  else
    warn "Could not download ${BASE_URL}/bin/rgt."
    rm -f "$TARGET.tmp" 2>/dev/null || true
  fi

  # The server image contains builds for every supported client platform. Do
  # not silently install @latest from a different release when one is missing.
  if [ "$installed" -ne 1 ]; then
    warn "No runnable rgt binary is available for this OS/architecture."
    warn "Ask the server operator to publish a matching build."
    exit 1
  fi

# ---------------------------------------------------------------------------
# 3. Verify rgt is reachable and runs.
# ---------------------------------------------------------------------------
if ! command -v rgt >/dev/null 2>&1; then
  warn "rgt was installed but is not on your PATH. Open a new shell or add its"
  warn "directory to PATH, then re-run this installer."
  exit 1
fi
step "CLI installed"
detail "Binary: $(command -v rgt)"

# ---------------------------------------------------------------------------
# 4. Wire the current project, so this one command is the whole setup.
# ---------------------------------------------------------------------------
# Only when we are standing in a project: "curl | sh" inherits whatever
# directory the teammate happened to be in, and connecting blindly would
# scatter .regent/ into home directories. A .git entry is the marker (it is a
# file, not a directory, inside worktrees and submodules). The server is open,
# so no token is involved.
# Wiring is unconditional: rgt connect wires the project it is standing in, and
# says what to do when it is not standing in one. See the Go comment on this
# template for why the installer no longer inspects the terminal.
CONNECT_LOG="${TARGET}.connect.$$"
rgt connect "{{.BaseURL}}" >"$CONNECT_LOG" 2>&1 &
connect_pid=$!
if ! spin_wait "$connect_pid" "Connecting this project"; then
  cat "$CONNECT_LOG" >&2
  rm -f "$CONNECT_LOG"
  warn "Setup did not finish. You can re-run it any time with:"
  warn "  rgt connect {{.BaseURL}}"
  exit 1
fi
rm -f "$CONNECT_LOG"
step "Project connected"

# ---------------------------------------------------------------------------
# 5. Verify, rather than assume.
# ---------------------------------------------------------------------------
# Wiring can succeed mechanically and still capture nothing, and every other
# rgt command exits 0 in that state. Whoever pasted this command is usually not
# whoever would notice the silence, so the command checks its own work and
# fails loudly instead of ending on an unearned success message.
DOCTOR_LOG="${TARGET}.doctor.$$"
rgt doctor --issues-only >"$DOCTOR_LOG" 2>&1 &
doctor_pid=$!
if ! spin_wait "$doctor_pid" "Verifying capture"; then
  cat "$DOCTOR_LOG" >&2
  rm -f "$DOCTOR_LOG"
  warn "Setup ran, but verification failed - see the report above."
  warn "Nothing will be captured until those problems are fixed."
  exit 1
fi
if grep -q '[^[:space:]]' "$DOCTOR_LOG"; then cat "$DOCTOR_LOG"; fi
rm -f "$DOCTOR_LOG"
step "Integration verified"

# ---------------------------------------------------------------------------
# 6. End on the one thing the installer cannot do.
# ---------------------------------------------------------------------------
# Everything above this line, the paste did. Loading the hooks is the agent's
# job, and it only loads the ones belonging to the directory it was started in
# — so an agent opened above this project loads that directory's settings and,
# because capture resolves its store from the session's working directory,
# records its work there instead. That is #27, in both of the wrong places it
# ended: a project that captured nothing, and then a project whose history was
# quietly accumulating one directory up.
#
# So the run ends with an instruction rather than a summary. It is the same
# instruction whether or not doctor found a shadowing directory above — doctor
# names that case specifically, and this is the move that answers it either way.
printf '\n'
printf '%s╭──────────────────────────────────────────────────────╮%s\n' "$GREEN" "$RESET"
printf '%s│%s  %s READY %s  %sReady to capture%s                       %s│%s\n' "$GREEN" "$RESET" "$GREEN$BOLD" "$RESET" "$BOLD" "$RESET" "$GREEN" "$RESET"
printf '%s╰──────────────────────────────────────────────────────╯%s\n' "$GREEN" "$RESET"
printf '  %sOne thing left:%s open the agent inside this project.\n' "$BOLD" "$RESET"
printf '  %s→%s  %sNEXT%s  cd %s && restart your agent\n' "$BLUE" "$RESET" "$BLUE$BOLD" "$RESET" "$(pwd)"
printf '              Then run rgt doctor\n'
detail "Agent command: cd $(pwd) && claude  # or codex, OpenCode, Pi"
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
	// The operator's declared public address wins over anything inferred
	// from the request: proxies drop ports and rewrite hosts, and a wrong
	// base here sends every teammate's install to the wrong place.
	if public := strings.TrimSpace(os.Getenv("REGENT_PUBLIC_URL")); public != "" {
		return strings.TrimRight(public, "/")
	}
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
