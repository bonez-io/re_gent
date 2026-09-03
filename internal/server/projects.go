package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ProjectSource is the client-computed identity of the source repository a
// project was enrolled from, per RFC 0004. Every field is optional: a
// directory that is not a git repository has no fingerprint at all and is
// always a new project.
type ProjectSource struct {
	Remote      string
	RootCommit  string
	Fingerprint string
}

// Project is one enrolled repository's history record. ID is immutable and is
// also the storage key StorageLocator resolves; DisplayName is the only
// mutable field.
type Project struct {
	ID          string
	TenantID    string
	DisplayName string
	CreatedAt   time.Time
	Source      ProjectSource
	// Visibility is "private" or "public". The default registry never sets a
	// project to "public": that is a managed-composition, GitHub-derived-role
	// feature (RFC 0004 "Open source projects") outside this stream's scope.
	Visibility string
}

// ProjectCreate is the input to ProjectRegistry.Create.
type ProjectCreate struct {
	Fingerprint string
	Remote      string
	RootCommit  string
	DisplayName string
}

// ErrProjectNotFound is returned by ProjectRegistry.Get and .Rename when the
// id (scoped to the tenant) has no row.
var ErrProjectNotFound = errors.New("project not found")

// ProjectRegistry is the seam RFC 0004 calls "project registry": the source of
// truth for project identity, replacing "POST /repos makes a directory named
// by the client" with server-generated ids, fingerprint-based idempotency, and
// a per-tenant listing.
//
// The default implementation (installed when no registry is configured) is
// filesystem+SQLite: it keeps a small dataDir/projects.db alongside the
// existing dataDir/repos/<id> layout, generates "prj_"-prefixed ids, and
// creates each project's storage through the configured StorageLocator. A
// managed composition supplies its own registry backed by its policy store
// (organizations, memberships, source fingerprints) and is not expected to use
// flat on-disk directories at all.
type ProjectRegistry interface {
	// Create registers a project. When req.Fingerprint is non-empty and a
	// project with that fingerprint already exists for tenantID, Create
	// returns the existing project with created=false instead of making a
	// duplicate — this is the RFC 0004 "connect once" guarantee. Otherwise it
	// creates a new project and returns created=true.
	Create(ctx context.Context, tenantID string, req ProjectCreate) (project Project, created bool, err error)

	// Get returns the project with the given id, scoped to tenantID.
	// ErrProjectNotFound when there is no such row for that tenant, which
	// callers should treat as if it does not exist for that tenant.
	Get(ctx context.Context, tenantID, id string) (Project, error)

	// LookupFingerprint returns the project already enrolled for tenantID
	// under fingerprint, or ErrProjectNotFound.
	LookupFingerprint(ctx context.Context, tenantID, fingerprint string) (Project, error)

	// LookupRootCommit returns every public project (in any tenant) whose
	// source root commit matches, for fork detection against an upstream open
	// source project (RFC 0004). The default registry never has a public
	// project, so it always returns an empty slice.
	LookupRootCommit(ctx context.Context, rootCommit string) ([]Project, error)

	// List returns every project belonging to tenantID, ordered by creation
	// time.
	List(ctx context.Context, tenantID string) ([]Project, error)

	// Rename changes a project's mutable display name. ErrProjectNotFound
	// when there is no such row for that tenant.
	Rename(ctx context.Context, tenantID, id, displayName string) error
}

var projectFingerprintRE = regexp.MustCompile(`^[0-9a-f]{1,128}$`)

// ValidateFingerprint reports whether raw is an acceptable source fingerprint:
// non-empty lowercase hex, as blake3 hex-digests always are.
func ValidateFingerprint(raw string) error {
	if !projectFingerprintRE.MatchString(raw) {
		return fmt.Errorf("invalid fingerprint %q: expected lowercase hex", raw)
	}
	return nil
}

// filesystemProjectRegistry is the default ProjectRegistry: a small SQLite
// database at dataDir/projects.db, opened lazily so a server that never uses
// the versioned project API never creates the file.
type filesystemProjectRegistry struct {
	path    string
	locator StorageLocator

	once sync.Once
	db   *sql.DB
	err  error
}

func newFilesystemProjectRegistry(dataDir string, locator StorageLocator) *filesystemProjectRegistry {
	return &filesystemProjectRegistry{path: filepath.Join(dataDir, "projects.db"), locator: locator}
}

func (r *filesystemProjectRegistry) open() (*sql.DB, error) {
	r.once.Do(func() {
		db, err := sql.Open("sqlite", r.path)
		if err != nil {
			r.err = fmt.Errorf("open project registry: %w", err)
			return
		}
		db.SetMaxOpenConns(1)
		for _, pragma := range []string{
			"PRAGMA journal_mode = WAL",
			"PRAGMA busy_timeout = 5000",
		} {
			if _, err := db.Exec(pragma); err != nil {
				r.err = fmt.Errorf("configure project registry: %w", err)
				return
			}
		}
		const schema = `
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL DEFAULT '',
  display_name TEXT NOT NULL,
  created_at TEXT NOT NULL,
  fingerprint TEXT NOT NULL DEFAULT '',
  remote TEXT NOT NULL DEFAULT '',
  root_commit TEXT NOT NULL DEFAULT '',
  visibility TEXT NOT NULL DEFAULT 'private',
  legacy INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS projects_tenant_fingerprint
  ON projects(tenant_id, fingerprint) WHERE fingerprint <> '';
CREATE INDEX IF NOT EXISTS projects_tenant ON projects(tenant_id, created_at);
CREATE INDEX IF NOT EXISTS projects_root_commit ON projects(root_commit) WHERE visibility = 'public';
`
		if _, err := db.Exec(schema); err != nil {
			r.err = fmt.Errorf("create project registry schema: %w", err)
			return
		}
		r.db = db
	})
	return r.db, r.err
}

func (r *filesystemProjectRegistry) Create(ctx context.Context, tenantID string, req ProjectCreate) (Project, bool, error) {
	db, err := r.open()
	if err != nil {
		return Project{}, false, err
	}
	if req.Fingerprint == "" && req.DisplayName == "" {
		return Project{}, false, errors.New("display_name is required when fingerprint is absent")
	}
	if req.Fingerprint != "" {
		if err := ValidateFingerprint(req.Fingerprint); err != nil {
			return Project{}, false, err
		}
		if existing, err := r.lookupFingerprintTx(ctx, db, tenantID, req.Fingerprint); err == nil {
			return existing, false, nil
		} else if !errors.Is(err, ErrProjectNotFound) {
			return Project{}, false, err
		}
	}

	id, err := newProjectID()
	if err != nil {
		return Project{}, false, err
	}
	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.Remote
	}
	if displayName == "" {
		displayName = id
	}
	project := Project{
		ID: id, TenantID: tenantID, DisplayName: displayName, CreatedAt: time.Now().UTC(),
		Source:     ProjectSource{Remote: req.Remote, RootCommit: req.RootCommit, Fingerprint: req.Fingerprint},
		Visibility: "private",
	}
	_, err = db.ExecContext(ctx, `INSERT INTO projects(id, tenant_id, display_name, created_at, fingerprint, remote, root_commit, visibility)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		project.ID, project.TenantID, project.DisplayName, formatProjectTime(project.CreatedAt),
		project.Source.Fingerprint, project.Source.Remote, project.Source.RootCommit, project.Visibility)
	if err != nil {
		// A concurrent enrollment of the same fingerprint lost the race above
		// but hit the unique index here: re-read and report the winner, which
		// is the same "connect once" guarantee as the pre-check.
		if req.Fingerprint != "" {
			if existing, lookupErr := r.lookupFingerprintTx(ctx, db, tenantID, req.Fingerprint); lookupErr == nil {
				return existing, false, nil
			}
		}
		return Project{}, false, fmt.Errorf("insert project: %w", err)
	}

	if r.locator != nil {
		root, err := r.locator.ProjectRoot(tenantID, id)
		if err != nil {
			return Project{}, false, fmt.Errorf("resolve project storage: %w", err)
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			return Project{}, false, fmt.Errorf("create project storage: %w", err)
		}
	}
	return project, true, nil
}

// ensureLegacyProject registers a project whose id is the client-supplied
// legacy repo id (POST /repos, and a pre-existing dataDir/repos/<id>
// directory found with no registry row). It is not part of the public
// ProjectRegistry interface: a composition with its own registry has no flat
// on-disk directories to reconcile against.
func (r *filesystemProjectRegistry) ensureLegacyProject(ctx context.Context, id string) (Project, bool, error) {
	db, err := r.open()
	if err != nil {
		return Project{}, false, err
	}
	if existing, err := r.getTx(ctx, db, "", id); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, ErrProjectNotFound) {
		return Project{}, false, err
	}
	now := time.Now().UTC()
	_, err = db.ExecContext(ctx, `INSERT INTO projects(id, tenant_id, display_name, created_at, visibility, legacy)
VALUES (?, '', ?, ?, 'private', 1)`, id, id, formatProjectTime(now))
	if err != nil {
		if existing, lookupErr := r.getTx(ctx, db, "", id); lookupErr == nil {
			return existing, false, nil
		}
		return Project{}, false, fmt.Errorf("insert legacy project: %w", err)
	}
	return Project{ID: id, DisplayName: id, CreatedAt: now, Visibility: "private"}, true, nil
}

func (r *filesystemProjectRegistry) Get(ctx context.Context, tenantID, id string) (Project, error) {
	db, err := r.open()
	if err != nil {
		return Project{}, err
	}
	return r.getTx(ctx, db, tenantID, id)
}

func (r *filesystemProjectRegistry) getTx(ctx context.Context, db *sql.DB, tenantID, id string) (Project, error) {
	row := db.QueryRowContext(ctx, `SELECT id, tenant_id, display_name, created_at, fingerprint, remote, root_commit, visibility
FROM projects WHERE id=? AND tenant_id=?`, id, tenantID)
	return scanProject(row)
}

func (r *filesystemProjectRegistry) LookupFingerprint(ctx context.Context, tenantID, fingerprint string) (Project, error) {
	db, err := r.open()
	if err != nil {
		return Project{}, err
	}
	return r.lookupFingerprintTx(ctx, db, tenantID, fingerprint)
}

func (r *filesystemProjectRegistry) lookupFingerprintTx(ctx context.Context, db *sql.DB, tenantID, fingerprint string) (Project, error) {
	row := db.QueryRowContext(ctx, `SELECT id, tenant_id, display_name, created_at, fingerprint, remote, root_commit, visibility
FROM projects WHERE tenant_id=? AND fingerprint=?`, tenantID, fingerprint)
	return scanProject(row)
}

func (r *filesystemProjectRegistry) LookupRootCommit(ctx context.Context, rootCommit string) ([]Project, error) {
	db, err := r.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, tenant_id, display_name, created_at, fingerprint, remote, root_commit, visibility
FROM projects WHERE root_commit=? AND visibility='public'`, rootCommit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProjectRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *filesystemProjectRegistry) List(ctx context.Context, tenantID string) ([]Project, error) {
	db, err := r.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, tenant_id, display_name, created_at, fingerprint, remote, root_commit, visibility
FROM projects WHERE tenant_id=? ORDER BY created_at ASC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProjectRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *filesystemProjectRegistry) Rename(ctx context.Context, tenantID, id, displayName string) error {
	if displayName == "" || len(displayName) > 200 {
		return errors.New("display_name must be 1-200 characters")
	}
	db, err := r.open()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE projects SET display_name=? WHERE id=? AND tenant_id=?`, displayName, id, tenantID)
	if err != nil {
		return fmt.Errorf("rename project: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProject(row *sql.Row) (Project, error) {
	return scanProjectRow(row)
}

func scanProjectRows(rows *sql.Rows) (Project, error) {
	return scanProjectRow(rows)
}

func scanProjectRow(scanner rowScanner) (Project, error) {
	var p Project
	var created string
	if err := scanner.Scan(&p.ID, &p.TenantID, &p.DisplayName, &created, &p.Source.Fingerprint, &p.Source.Remote, &p.Source.RootCommit, &p.Visibility); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, ErrProjectNotFound
		}
		return Project{}, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return p, nil
}

func formatProjectTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func newProjectID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate project id: %w", err)
	}
	return "prj_" + hex.EncodeToString(b), nil
}
