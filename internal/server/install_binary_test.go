package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestBinaryServesPerOSArch asserts GET /bin/rgt?os=&arch= serves the prebuilt
// binary matching the requested platform from the binaries dir, names the
// Windows download rgt.exe, rejects platforms outside the allow-list, and 404s
// a cross-platform request with no prebuilt binary (so the installer falls back
// to building from source).
func TestBinaryServesPerOSArch(t *testing.T) {
	binDir := t.TempDir()
	// Fake binaries whose bytes we can assert we got back verbatim.
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(content), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("rgt_darwin_arm64", "MACHO-ARM64")
	write("rgt_linux_amd64", "ELF-AMD64")
	write("rgt_windows_amd64.exe", "PE-AMD64")

	_, _, ts := newTestServer(t, WithBinariesDir(binDir))

	get := func(query string) (*http.Response, []byte) {
		t.Helper()
		resp, err := http.Get(ts.URL + "/bin/rgt" + query)
		if err != nil {
			t.Fatalf("GET /bin/rgt%s: %v", query, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, body
	}

	// Each requested platform gets its own prebuilt binary, byte-for-byte.
	for _, tc := range []struct{ query, want string }{
		{"?os=darwin&arch=arm64", "MACHO-ARM64"},
		{"?os=linux&arch=amd64", "ELF-AMD64"},
		{"?os=windows&arch=amd64", "PE-AMD64"},
	} {
		resp, body := get(tc.query)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d, want 200", tc.query, resp.StatusCode)
		}
		if string(body) != tc.want {
			t.Errorf("%s: body %q, want %q", tc.query, body, tc.want)
		}
	}

	// Windows downloads keep their .exe name so the client saves an executable.
	if resp, _ := get("?os=windows&arch=amd64"); !strings.Contains(resp.Header.Get("Content-Disposition"), "rgt.exe") {
		t.Errorf("windows Content-Disposition = %q, want it to name rgt.exe", resp.Header.Get("Content-Disposition"))
	}

	// A platform outside the allow-list is a 400, not a path lookup — this is
	// also what stops a traversal attempt through os/arch.
	for _, bad := range []string{"?os=freebsd&arch=amd64", "?os=..&arch=..", "?os=linux&arch=mips"} {
		if resp, _ := get(bad); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET /bin/rgt%s: status %d, want 400", bad, resp.StatusCode)
		}
	}

	// An allow-listed platform with no prebuilt binary, that also isn't the
	// server's own platform, 404s so the installer builds from source.
	if miss, ok := missingTarget(binDir); ok {
		q := "?os=" + miss[0] + "&arch=" + miss[1]
		if resp, _ := get(q); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET /bin/rgt%s: status %d, want 404", q, resp.StatusCode)
		}
	}
}

// TestBinaryDefaultsToOwnPlatform asserts that with no os/arch and no binaries
// dir, /bin/rgt still serves the server's own executable (original behavior).
func TestBinaryDefaultsToOwnPlatform(t *testing.T) {
	_, _, ts := newTestServer(t) // no binaries dir
	resp, err := http.Get(ts.URL + "/bin/rgt")
	if err != nil {
		t.Fatalf("GET /bin/rgt: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status %d, want 200 (serves the server's own executable)", resp.StatusCode)
	}
}

// missingTarget returns an allow-listed target that has no file in binDir and
// isn't the server's own platform (so it must 404), or ok=false if none exists.
func missingTarget(binDir string) ([2]string, bool) {
	for tgt, name := range binaryTargets {
		if tgt[0] == runtime.GOOS && tgt[1] == runtime.GOARCH {
			continue
		}
		if _, err := os.Stat(filepath.Join(binDir, name)); os.IsNotExist(err) {
			return tgt, true
		}
	}
	return [2]string{}, false
}
