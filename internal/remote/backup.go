package remote

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// maxBackupErrorBytes bounds how much of a non-200 backup response this
// package will read to build an error message. The tar body itself, on
// success, is streamed straight to disk instead and is not bounded here.
const maxBackupErrorBytes = 16 << 10

// DownloadBackup calls POST /api/v1/admin/backup (RFC 0005 Appendix A,
// "Backup"), authenticated with an admin's stored credential, and streams
// the response — an application/x-tar of identity.db and projects.db — to a
// new file at outPath with mode 0600, since it carries the whole identity
// and project database. It returns the number of bytes written.
//
// Whether outPath may already exist is the caller's decision (`rgt admin
// backup`'s --force), made before this is called; DownloadBackup always
// truncates. A failure partway through removes the partial file, so a failed
// backup never leaves a truncated one behind that a later run — or a person —
// could mistake for complete.
func DownloadBackup(ctx context.Context, client *http.Client, serverURL, token, outPath string) (int64, error) {
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := strings.TrimRight(serverURL, "/") + "/api/v1/admin/backup"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("build backup request: %w", err)
	}
	req.Header.Set("Accept", "application/x-tar")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("POST %s: %w", redactURL(endpoint), err)
	}
	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxBackupErrorBytes))
		return 0, decodeServerError(resp.StatusCode, data)
	}

	f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", outPath, err)
	}
	// Belt and braces: a pre-existing file's mode is not always overridden by
	// the O_CREATE mode on every platform, and this file is deliberately
	// owner-only.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(outPath)
		return 0, fmt.Errorf("chmod %s: %w", outPath, err)
	}

	written, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(outPath)
		return 0, fmt.Errorf("write %s: %w", outPath, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(outPath)
		return 0, fmt.Errorf("close %s: %w", outPath, closeErr)
	}
	return written, nil
}
