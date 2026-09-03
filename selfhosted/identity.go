// Package selfhosted provides the production-capable, single-node re_gent
// composition. It adds persistent local identities, personal access tokens,
// browser sessions, project roles, and audit events around the public server
// core.
package selfhosted

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/bonez-io/re_gent/serverauth"
	_ "modernc.org/sqlite"
)

const (
	personalTokenPrefix = "rgt_pat_"
	sessionTokenPrefix  = "rgt_session_"
	defaultPATLifetime  = 30 * 24 * time.Hour
	sessionLifetime     = 12 * time.Hour
	setupCodeLifetime   = 15 * time.Minute
	invitationLifetime  = 7 * 24 * time.Hour
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}[a-z0-9]$|^[a-z0-9]{1,2}$`)

// Role is one of the fixed self-hosted project roles.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleWriter Role = "writer"
	RoleReader Role = "reader"
)

// User is the public, non-secret representation of a self-hosted identity.
type User struct {
	ID            string    `json:"id"`
	Username      string    `json:"username"`
	DisplayName   string    `json:"display_name"`
	Email         string    `json:"email,omitempty"`
	InstanceOwner bool      `json:"instance_owner"`
	OrgRole       string    `json:"-"`
	CreatedAt     time.Time `json:"created_at"`
}

// Member is one project membership plus the associated public user fields.
type Member struct {
	User
	Role Role `json:"role"`
}

// Token describes a credential without exposing its secret.
type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type identityStore struct {
	db  *sql.DB
	now func() time.Time
}

type authenticated struct {
	principal              serverauth.Principal
	user                   User
	tokenID                string
	tokenKind              string
	tokenName              string
	csrf                   string
	passwordChangeRequired bool
}

// RecoverOwnerToken issues a new owner PAT for an existing self-hosted data
// directory. Operators should stop the server first and run this only from the
// host that owns the mode-0600 identity database. Existing tokens are not
// revoked automatically.
func RecoverOwnerToken(dataDir string) (Token, string, error) {
	path := filepath.Join(dataDir, "identity.db")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Token{}, "", errors.New("identity database does not exist; start the server and complete bootstrap first")
		}
		return Token{}, "", err
	}
	store, err := openIdentityStore(path)
	if err != nil {
		return Token{}, "", err
	}
	defer func() { _ = store.close() }()
	var ownerID string
	if err := store.db.QueryRow("SELECT id FROM users WHERE instance_owner=1 AND disabled_at IS NULL").Scan(&ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Token{}, "", errors.New("no active instance owner exists")
		}
		return Token{}, "", err
	}
	return store.issuePAT("operator-recovery", ownerID, "operator recovery", 24*time.Hour)
}

func openIdentityStore(path string) (*identityStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open identity database: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure identity database: %w", err)
		}
	}
	store := &identityStore{db: db, now: func() time.Time { return time.Now().UTC() }}
	if err := store.createSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("protect identity database: %w", err)
	}
	return store, nil
}

func (s *identityStore) createSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL COLLATE NOCASE UNIQUE,
  display_name TEXT NOT NULL,
  instance_owner INTEGER NOT NULL DEFAULT 0 CHECK (instance_owner IN (0, 1)),
  created_at TEXT NOT NULL,
  disabled_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS users_one_instance_owner
  ON users(instance_owner) WHERE instance_owner = 1;
CREATE TABLE IF NOT EXISTS credentials (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('pat', 'session')),
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,
  secret_hash BLOB NOT NULL UNIQUE,
  csrf_token TEXT,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  last_used_at TEXT,
  revoked_at TEXT
);
CREATE INDEX IF NOT EXISTS credentials_user ON credentials(user_id, kind, created_at);
CREATE TABLE IF NOT EXISTS memberships (
  repo_id TEXT NOT NULL,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'writer', 'reader')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (repo_id, user_id)
);
CREATE TABLE IF NOT EXISTS audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_id TEXT,
  action TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id TEXT NOT NULL,
  outcome TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS passwords (
  user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS instance_state (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  initial_admin_username TEXT NOT NULL,
  initial_admin_password_hash TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS organizations (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE COLLATE NOCASE,
  display_name TEXT NOT NULL,
  server_url TEXT NOT NULL DEFAULT '',
  join_policy TEXT NOT NULL DEFAULT 'invite_only' CHECK (join_policy IN ('invite_only', 'open')),
  default_role TEXT NOT NULL DEFAULT 'reader',
  onboarding_state TEXT NOT NULL DEFAULT 'connect' CHECK (onboarding_state IN ('connect', 'users', 'done')),
  allowed_github_orgs TEXT NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS auth_method_settings (
  org_id TEXT PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  github_enabled INTEGER NOT NULL DEFAULT 0,
  github_client_id TEXT NOT NULL DEFAULT '',
  github_client_secret_enc BLOB,
  github_base_url TEXT NOT NULL DEFAULT '',
  google_enabled INTEGER NOT NULL DEFAULT 0,
  google_client_id TEXT NOT NULL DEFAULT '',
  google_client_secret_enc BLOB,
  smtp_enabled INTEGER NOT NULL DEFAULT 0,
  smtp_host TEXT NOT NULL DEFAULT '',
  smtp_port INTEGER NOT NULL DEFAULT 0,
  smtp_username TEXT NOT NULL DEFAULT '',
  smtp_password_enc BLOB,
  smtp_from TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS setup_codes (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash BLOB NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  used_at TEXT
);
CREATE TABLE IF NOT EXISTS invitations (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  email TEXT NOT NULL DEFAULT '',
  username TEXT NOT NULL DEFAULT '',
  org_role TEXT NOT NULL CHECK (org_role IN ('admin', 'member')),
  grants TEXT NOT NULL DEFAULT '[]',
  token_hash BLOB NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'expired', 'revoked')),
  invited_by TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  accepted_at TEXT,
  revoked_at TEXT
);
CREATE TABLE IF NOT EXISTS identities (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  subject TEXT NOT NULL,
  login TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY (provider, subject)
);
CREATE TABLE IF NOT EXISTS connections (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL,
  display_name TEXT NOT NULL,
  remote TEXT NOT NULL DEFAULT '',
  machine_name TEXT NOT NULL DEFAULT '',
  connected_by TEXT NOT NULL,
  connected_at TEXT NOT NULL
);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("create identity schema: %w", err)
	}
	// Additive migration: the serverauth.Auditor seam (RFC 0004) needs a
	// request id, an actor classification, and tenant/project scope on every
	// row. These default to '' / 'user' so every pre-existing row (and every
	// existing appendAuditTx call site, which does not set them) stays valid;
	// only new callers populate them.
	if err := s.ensureAuditColumns(); err != nil {
		return fmt.Errorf("extend audit_events schema: %w", err)
	}
	// Additive migration: RFC 0005 self-hosted onboarding needs an
	// organization role (distinct from the per-project membership role) and a
	// flag that gates every non-auth route until the admin has replaced the
	// initial random password. Both default to values that keep every
	// pre-existing row valid: 'member' (an instance owner is also granted
	// 'admin' explicitly wherever it is read) and 0 (no pre-existing user is
	// retroactively locked out).
	if err := s.ensureUsersColumns(); err != nil {
		return fmt.Errorf("extend users schema: %w", err)
	}
	return nil
}

// ensureUsersColumns is ensureAuditColumns' sibling for the users table; see
// its comment for why PRAGMA table_info is used instead of "ADD COLUMN IF NOT
// EXISTS".
func (s *identityStore) ensureUsersColumns() error {
	rows, err := s.db.Query(`PRAGMA table_info(users)`)
	if err != nil {
		return err
	}
	existing := make(map[string]bool)
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		existing[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	additions := []struct{ name, ddl string }{
		{"org_role", `ALTER TABLE users ADD COLUMN org_role TEXT NOT NULL DEFAULT 'member'`},
		{"password_change_required", `ALTER TABLE users ADD COLUMN password_change_required INTEGER NOT NULL DEFAULT 0`},
		{"email", `ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT ''`},
	}
	for _, add := range additions {
		if existing[add.name] {
			continue
		}
		if _, err := s.db.Exec(add.ddl); err != nil {
			return err
		}
	}
	return nil
}

// ensureAuditColumns adds any audit_events column this version of selfhosted
// expects but an older database does not have yet. It checks PRAGMA
// table_info first rather than relying on "ADD COLUMN IF NOT EXISTS": that
// syntax is not supported by the SQLite version modernc.org/sqlite v1.28.0
// embeds, and plain ADD COLUMN errors on a column that already exists.
func (s *identityStore) ensureAuditColumns() error {
	rows, err := s.db.Query(`PRAGMA table_info(audit_events)`)
	if err != nil {
		return err
	}
	existing := make(map[string]bool)
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		existing[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	additions := []struct{ name, ddl string }{
		{"request_id", `ALTER TABLE audit_events ADD COLUMN request_id TEXT NOT NULL DEFAULT ''`},
		{"actor_kind", `ALTER TABLE audit_events ADD COLUMN actor_kind TEXT NOT NULL DEFAULT 'user'`},
		{"tenant_id", `ALTER TABLE audit_events ADD COLUMN tenant_id TEXT NOT NULL DEFAULT ''`},
		{"project_id", `ALTER TABLE audit_events ADD COLUMN project_id TEXT NOT NULL DEFAULT ''`},
	}
	for _, add := range additions {
		if existing[add.name] {
			continue
		}
		if _, err := s.db.Exec(add.ddl); err != nil {
			return err
		}
	}
	return nil
}

func (s *identityStore) close() error { return s.db.Close() }

// adminSetup describes the outcome of ensureInitialAdmin: whether this run
// generated a brand-new initial password (in which case Password carries the
// plaintext, to be printed exactly once) or an admin already existed from a
// previous run (Password is always "" in that case: only the Argon2id hash is
// ever persisted, so a restart cannot reprint a plaintext it no longer has).
type adminSetup struct {
	Username             string
	Password             string
	Generated            bool
	PasswordChangeNeeded bool
}

// ensureInitialAdmin creates the user "admin" (or requestedUsername, when
// non-empty) with a random 20-character password on a truly empty instance,
// or reports the state of the admin already created by an earlier run.
// requestedPassword, when non-empty, is used instead of a random one (the
// REGENT_ADMIN_PASSWORD env var / --admin-password flag path).
func (s *identityStore) ensureInitialAdmin(requestedUsername, requestedPassword string) (adminSetup, error) {
	if requestedUsername == "" {
		requestedUsername = "admin"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return adminSetup{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var existingUsername string
	err = tx.QueryRow("SELECT initial_admin_username FROM instance_state WHERE singleton=1").Scan(&existingUsername)
	switch {
	case err == nil:
		var required int
		if scanErr := tx.QueryRow("SELECT password_change_required FROM users WHERE username=? COLLATE NOCASE", existingUsername).Scan(&required); scanErr != nil {
			return adminSetup{}, fmt.Errorf("read admin password state: %w", scanErr)
		}
		return adminSetup{Username: existingUsername, PasswordChangeNeeded: required == 1}, nil
	case errors.Is(err, sql.ErrNoRows):
		// Fresh instance, or an instance bootstrapped before passwords
		// existed: fall through.
	default:
		return adminSetup{}, fmt.Errorf("read instance state: %w", err)
	}

	password := requestedPassword
	generated := false
	if password == "" {
		password, err = newRandomPassword(20)
		if err != nil {
			return adminSetup{}, err
		}
		generated = true
	}
	hash, err := hashPassword(password)
	if err != nil {
		return adminSetup{}, err
	}
	now := s.now()

	// Upgrade path: a data directory from the bootstrap-token era already
	// has an instance owner but no instance_state row and no password.
	// Creating a second owner would violate the one-owner rule (and did,
	// crash-looping the server on every pre-RFC-0005 volume), so the
	// existing owner is adopted as the admin: they get the initial password
	// printed exactly as a fresh install would, must replace it on first
	// browser sign-in, and their existing tokens keep working, because the
	// password gate applies to browser sessions only.
	var ownerID, ownerUsername string
	switch err := tx.QueryRow("SELECT id, username FROM users WHERE instance_owner=1 LIMIT 1").Scan(&ownerID, &ownerUsername); {
	case err == nil:
		if _, err := tx.Exec(`INSERT INTO passwords(user_id, hash, created_at, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT(user_id) DO UPDATE SET hash=excluded.hash, updated_at=excluded.updated_at`, ownerID, hash, formatTime(now), formatTime(now)); err != nil {
			return adminSetup{}, fmt.Errorf("store adopted owner password: %w", err)
		}
		if _, err := tx.Exec(`UPDATE users SET password_change_required=1, org_role='admin' WHERE id=?`, ownerID); err != nil {
			return adminSetup{}, fmt.Errorf("adopt owner as admin: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO instance_state(singleton, initial_admin_username, initial_admin_password_hash, created_at)
VALUES (1, ?, ?, ?)`, ownerUsername, hash, formatTime(now)); err != nil {
			return adminSetup{}, fmt.Errorf("store instance state: %w", err)
		}
		if err := appendAuditTx(tx, ownerID, "instance.adopt_owner", "user", ownerID, "success", now); err != nil {
			return adminSetup{}, err
		}
		if err := tx.Commit(); err != nil {
			return adminSetup{}, err
		}
		return adminSetup{Username: ownerUsername, Password: password, Generated: generated, PasswordChangeNeeded: true}, nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return adminSetup{}, fmt.Errorf("read instance owner: %w", err)
	}

	username, _, err := validateUserInput(requestedUsername, requestedUsername)
	if err != nil {
		return adminSetup{}, fmt.Errorf("invalid admin username %q: %w", requestedUsername, err)
	}
	userID, err := newID("usr")
	if err != nil {
		return adminSetup{}, err
	}
	if _, err := tx.Exec(`INSERT INTO users(id, username, display_name, instance_owner, org_role, password_change_required, created_at)
VALUES (?, ?, ?, 1, 'admin', 1, ?)`, userID, username, username, formatTime(now)); err != nil {
		return adminSetup{}, fmt.Errorf("create initial admin: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO passwords(user_id, hash, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		userID, hash, formatTime(now), formatTime(now)); err != nil {
		return adminSetup{}, fmt.Errorf("store initial admin password: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO instance_state(singleton, initial_admin_username, initial_admin_password_hash, created_at)
VALUES (1, ?, ?, ?)`, username, hash, formatTime(now)); err != nil {
		return adminSetup{}, fmt.Errorf("store instance state: %w", err)
	}
	if err := appendAuditTx(tx, userID, "instance.bootstrap", "user", userID, "success", now); err != nil {
		return adminSetup{}, err
	}
	if err := tx.Commit(); err != nil {
		return adminSetup{}, err
	}
	return adminSetup{Username: username, Password: password, Generated: generated, PasswordChangeNeeded: true}, nil
}

func (s *identityStore) Authenticate(r *http.Request) (serverauth.Principal, error) {
	auth, err := s.authenticate(r)
	if err != nil {
		return serverauth.Principal{}, err
	}
	return auth.principal, nil
}

func (s *identityStore) authenticate(r *http.Request) (authenticated, error) {
	secret := ""
	wantKind := "pat"
	authMethod := "pat"
	values := r.Header.Values("Authorization")
	if len(values) > 1 {
		return authenticated{}, serverauth.ErrUnauthenticated
	}
	if len(values) == 1 {
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return authenticated{}, serverauth.ErrUnauthenticated
		}
		secret = parts[1]
	} else if cookie, err := r.Cookie(sessionCookieName); err == nil {
		secret = cookie.Value
		wantKind = "session"
		authMethod = "session"
	} else {
		// Neither an Authorization header nor a session cookie was presented
		// at all, as opposed to one being present and wrong: this is the
		// specific "no credentials" case the public core's anonymous-
		// principal flow (serverauth.ErrNoCredentials) distinguishes from bad
		// credentials. It wraps ErrUnauthenticated, so Authorize below (and
		// every writeAccessError call in this package) still denies it with
		// exactly the same 401 it always has.
		return authenticated{}, serverauth.ErrNoCredentials
	}
	if (wantKind == "pat" && !strings.HasPrefix(secret, personalTokenPrefix)) ||
		(wantKind == "session" && !strings.HasPrefix(secret, sessionTokenPrefix)) {
		return authenticated{}, serverauth.ErrUnauthenticated
	}

	var auth authenticated
	var instanceOwner, passwordChangeRequired int
	var createdAt, expiresAt string
	var csrf sql.NullString
	err := s.db.QueryRow(`SELECT c.id, c.kind, c.csrf_token, c.name,
u.id, u.username, u.display_name, u.email, u.instance_owner, u.org_role, u.password_change_required, u.created_at, c.expires_at
FROM credentials c JOIN users u ON u.id=c.user_id
WHERE c.secret_hash=? AND c.kind=? AND c.revoked_at IS NULL AND u.disabled_at IS NULL`,
		hashSecret(secret), wantKind).Scan(&auth.tokenID, &auth.tokenKind, &csrf, &auth.tokenName,
		&auth.user.ID, &auth.user.Username, &auth.user.DisplayName, &auth.user.Email, &instanceOwner, &auth.user.OrgRole, &passwordChangeRequired, &createdAt, &expiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return authenticated{}, serverauth.ErrUnauthenticated
		}
		return authenticated{}, err
	}
	expires, err := parseTime(expiresAt)
	if err != nil || !expires.After(s.now()) {
		return authenticated{}, serverauth.ErrUnauthenticated
	}
	auth.user.CreatedAt, _ = parseTime(createdAt)
	auth.user.InstanceOwner = instanceOwner == 1
	// The initial-password gate is a browser-session concern: it exists so
	// the printed password cannot be used for anything but replacing itself.
	// Tokens are only ever minted after that replacement on a fresh install,
	// and on an upgraded install they predate passwords entirely and must
	// keep the owner's hooks capturing, so they are never gated.
	auth.passwordChangeRequired = passwordChangeRequired == 1 && wantKind == "session"
	auth.csrf = csrf.String
	if wantKind == "session" && isUnsafeMethod(r.Method) {
		provided := r.Header.Get(csrfHeaderName)
		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(auth.csrf)) != 1 {
			return authenticated{}, serverauth.ErrForbidden
		}
	}
	roles := []string{}
	if auth.user.InstanceOwner {
		roles = append(roles, "instance_owner")
	}
	if auth.user.OrgRole == "admin" {
		roles = append(roles, "org_admin")
	}
	if auth.passwordChangeRequired {
		roles = append(roles, "password_change_required")
	}
	auth.principal = serverauth.Principal{Subject: auth.user.ID, Roles: roles, AuthMethod: authMethod}
	_, _ = s.db.Exec("UPDATE credentials SET last_used_at=? WHERE id=?", formatTime(s.now()), auth.tokenID)
	return auth, nil
}

// errPasswordChangeRequired wraps serverauth.ErrForbidden so every existing
// errors.Is(err, serverauth.ErrForbidden) check (including the public core's
// own writeAccessError for object/ref/history/project routes) still treats
// it as a 403, while selfhosted's own writeAccessError recognizes it
// specifically and reports the "password_change_required" code RFC 0005
// requires.
var errPasswordChangeRequired = fmt.Errorf("password change required: %w", serverauth.ErrForbidden)

func (s *identityStore) Authorize(_ context.Context, principal serverauth.Principal, permission serverauth.Permission) error {
	if principal.Subject == "" {
		return serverauth.ErrUnauthenticated
	}
	if hasRole(principal.Roles, "password_change_required") && permission.Action != serverauth.ActionIdentityRead {
		return errPasswordChangeRequired
	}
	owner := hasRole(principal.Roles, "instance_owner")
	switch permission.Action {
	case serverauth.ActionRepositoriesList, serverauth.ActionSkillList, serverauth.ActionSkillRead:
		return nil
	case serverauth.ActionIdentityRead, serverauth.ActionTokenRead, serverauth.ActionTokenWrite:
		if permission.Resource.Name == "" || permission.Resource.Name == principal.Subject {
			return nil
		}
		return serverauth.ErrNotFound
	case serverauth.ActionUserList, serverauth.ActionUserCreate:
		if owner {
			return nil
		}
		return serverauth.ErrForbidden
	case serverauth.ActionRepositoryCreate:
		if owner {
			return nil
		}
		return serverauth.ErrForbidden
	case serverauth.ActionRequest:
		return serverauth.ErrForbidden
	}
	if permission.Resource.RepositoryID == "" {
		return serverauth.ErrForbidden
	}
	if owner {
		return nil
	}
	role, err := s.roleFor(permission.Resource.RepositoryID, principal.Subject)
	if err != nil {
		return err
	}
	var minimum Role
	switch permission.Action {
	case serverauth.ActionObjectWrite, serverauth.ActionRefWrite, serverauth.ActionHistoryWrite:
		minimum = RoleWriter
	case serverauth.ActionMemberWrite, serverauth.ActionRepositoryWrite:
		minimum = RoleAdmin
	case serverauth.ActionRepositoryRead, serverauth.ActionObjectRead, serverauth.ActionRefRead, serverauth.ActionHistoryRead, serverauth.ActionMemberRead:
		minimum = RoleReader
	default:
		return serverauth.ErrForbidden
	}
	if roleRank(role) < roleRank(minimum) {
		return serverauth.ErrForbidden
	}
	return nil
}

func (s *identityStore) roleFor(repoID, userID string) (Role, error) {
	var role string
	if err := s.db.QueryRow("SELECT role FROM memberships WHERE repo_id=? AND user_id=?", repoID, userID).Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", serverauth.ErrNotFound
		}
		return "", err
	}
	return Role(role), nil
}

func (s *identityStore) listUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT id, username, display_name, instance_owner, created_at FROM users WHERE disabled_at IS NULL ORDER BY username COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var user User
		var owner int
		var created string
		if err := rows.Scan(&user.ID, &user.Username, &user.DisplayName, &owner, &created); err != nil {
			return nil, err
		}
		user.InstanceOwner = owner == 1
		user.CreatedAt, _ = parseTime(created)
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *identityStore) createUser(actorID, username, displayName, repoID string, role Role) (User, string, error) {
	username, displayName, err := validateUserInput(username, displayName)
	if err != nil {
		return User{}, "", err
	}
	if repoID != "" && roleRank(role) == 0 {
		return User{}, "", fmt.Errorf("invalid role %q", role)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	userID, err := newID("usr")
	if err != nil {
		return User{}, "", err
	}
	user := User{ID: userID, Username: username, DisplayName: displayName, CreatedAt: now}
	if _, err := tx.Exec("INSERT INTO users(id, username, display_name, created_at) VALUES (?, ?, ?, ?)", user.ID, user.Username, user.DisplayName, formatTime(now)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return User{}, "", fmt.Errorf("username already exists")
		}
		return User{}, "", err
	}
	secret, token, _, err := issueCredentialTx(tx, user.ID, "pat", "initial access", now, now.Add(defaultPATLifetime))
	if err != nil {
		return User{}, "", err
	}
	if err := appendAuditTx(tx, actorID, "user.create", "user", user.ID, "success", now); err != nil {
		return User{}, "", err
	}
	if err := appendAuditTx(tx, actorID, "token.create", "token", token.ID, "success", now); err != nil {
		return User{}, "", err
	}
	if repoID != "" {
		formattedNow := formatTime(now)
		if _, err := tx.Exec(`INSERT INTO memberships(repo_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, repoID, user.ID, role, formattedNow, formattedNow); err != nil {
			return User{}, "", err
		}
		if err := appendAuditTx(tx, actorID, "membership.put", "membership", repoID+":"+user.ID, "success", now); err != nil {
			return User{}, "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return User{}, "", err
	}
	return user, secret, nil
}

func (s *identityStore) listTokens(userID string) ([]Token, error) {
	rows, err := s.db.Query(`SELECT id, name, prefix, created_at, expires_at, last_used_at FROM credentials
WHERE user_id=? AND kind='pat' AND revoked_at IS NULL AND expires_at>? ORDER BY created_at DESC`, userID, formatTime(s.now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []Token{}
	for rows.Next() {
		var token Token
		var created, expires string
		var used sql.NullString
		if err := rows.Scan(&token.ID, &token.Name, &token.Prefix, &created, &expires, &used); err != nil {
			return nil, err
		}
		token.CreatedAt, _ = parseTime(created)
		token.ExpiresAt, _ = parseTime(expires)
		if used.Valid {
			t, _ := parseTime(used.String)
			token.LastUsedAt = &t
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (s *identityStore) issuePAT(actorID, userID, name string, lifetime time.Duration) (Token, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return Token{}, "", fmt.Errorf("token name must be 1-80 characters")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return Token{}, "", fmt.Errorf("token name must not contain control characters")
	}
	if lifetime < 24*time.Hour || lifetime > 365*24*time.Hour {
		return Token{}, "", fmt.Errorf("token lifetime must be between 1 and 365 days")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Token{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	secret, token, _, err := issueCredentialTx(tx, userID, "pat", name, now, now.Add(lifetime))
	if err != nil {
		return Token{}, "", err
	}
	if err := appendAuditTx(tx, actorID, "token.create", "token", token.ID, "success", now); err != nil {
		return Token{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return Token{}, "", err
	}
	return token, secret, nil
}

func (s *identityStore) revokeToken(actorID, userID, tokenID string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	result, err := tx.Exec(`UPDATE credentials SET revoked_at=? WHERE id=? AND user_id=? AND kind='pat' AND revoked_at IS NULL`, formatTime(now), tokenID, userID)
	if err != nil {
		return false, err
	}
	count, _ := result.RowsAffected()
	if count != 0 {
		if err := appendAuditTx(tx, actorID, "token.revoke", "token", tokenID, "success", now); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *identityStore) createSession(userID string) (string, string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	secret, _, csrf, err := issueCredentialTx(tx, userID, "session", "browser session", now, now.Add(sessionLifetime))
	if err != nil {
		return "", "", err
	}
	if err := appendAuditTx(tx, userID, "session.create", "user", userID, "success", now); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return secret, csrf, nil
}

func (s *identityStore) revokeSession(auth authenticated) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	if _, err := tx.Exec("UPDATE credentials SET revoked_at=? WHERE id=? AND kind='session'", formatTime(now), auth.tokenID); err != nil {
		return err
	}
	if err := appendAuditTx(tx, auth.user.ID, "session.revoke", "session", auth.tokenID, "success", now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *identityStore) listMembers(repoID string) ([]Member, error) {
	rows, err := s.db.Query(`SELECT u.id, u.username, u.display_name, u.instance_owner, u.created_at,
CASE WHEN u.instance_owner=1 THEN 'owner' ELSE m.role END
FROM users u LEFT JOIN memberships m ON m.user_id=u.id AND m.repo_id=?
WHERE u.disabled_at IS NULL AND (u.instance_owner=1 OR m.repo_id IS NOT NULL)
ORDER BY u.username COLLATE NOCASE`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []Member{}
	for rows.Next() {
		var member Member
		var owner int
		var created string
		if err := rows.Scan(&member.ID, &member.Username, &member.DisplayName, &owner, &created, &member.Role); err != nil {
			return nil, err
		}
		member.InstanceOwner = owner == 1
		member.CreatedAt, _ = parseTime(created)
		members = append(members, member)
	}
	return members, rows.Err()
}

func (s *identityStore) putMember(actorID, repoID, userID string, role Role) error {
	if roleRank(role) == 0 {
		return fmt.Errorf("invalid role %q", role)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM users WHERE id=? AND disabled_at IS NULL", userID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("user not found")
	}
	now := formatTime(s.now())
	if _, err := tx.Exec(`INSERT INTO memberships(repo_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(repo_id, user_id) DO UPDATE SET role=excluded.role, updated_at=excluded.updated_at`, repoID, userID, role, now, now); err != nil {
		return err
	}
	if err := appendAuditTx(tx, actorID, "membership.put", "membership", repoID+":"+userID, "success", s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *identityStore) deleteMember(actorID, repoID, userID string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec("DELETE FROM memberships WHERE repo_id=? AND user_id=?", repoID, userID)
	if err != nil {
		return false, err
	}
	count, _ := result.RowsAffected()
	if count != 0 {
		if err := appendAuditTx(tx, actorID, "membership.delete", "membership", repoID+":"+userID, "success", s.now()); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return count != 0, nil
}

func issueCredentialTx(tx *sql.Tx, userID, kind, name string, createdAt, expiresAt time.Time) (string, Token, string, error) {
	prefix := personalTokenPrefix
	if kind == "session" {
		prefix = sessionTokenPrefix
	}
	secret, err := newSecret(prefix)
	if err != nil {
		return "", Token{}, "", err
	}
	csrf := ""
	if kind == "session" {
		csrf, err = newSecret("csrf_")
		if err != nil {
			return "", Token{}, "", err
		}
	}
	tokenID, err := newID("tok")
	if err != nil {
		return "", Token{}, "", err
	}
	token := Token{ID: tokenID, Name: name, Prefix: secret[:minInt(len(secret), len(prefix)+8)], CreatedAt: createdAt, ExpiresAt: expiresAt}
	if _, err := tx.Exec(`INSERT INTO credentials(id, user_id, kind, name, prefix, secret_hash, csrf_token, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, token.ID, userID, kind, name, token.Prefix, hashSecret(secret), nullableString(csrf), formatTime(createdAt), formatTime(expiresAt)); err != nil {
		return "", Token{}, "", err
	}
	return secret, token, csrf, nil
}

// appendAuditTx records a mutation selfhosted performs directly (bootstrap,
// user/token/membership management) inside the same transaction as the
// mutation itself, so the audit row and the change it describes commit or
// roll back together. It is the same audit_events table Record (below) writes
// into for core-driven events; this call site's actor is always a signed-in
// user, so actor_kind defaults to "user" and tenant/project/request scope
// default to "" (self-hosted has one implicit tenant and these routes are not
// project-scoped the way object/ref/history routes are).
func appendAuditTx(tx *sql.Tx, actor, action, targetType, targetID, outcome string, at time.Time) error {
	_, err := tx.Exec(`INSERT INTO audit_events(actor_id, action, target_type, target_id, outcome, created_at, actor_kind)
VALUES (?, ?, ?, ?, ?, ?, 'user')`, actor, action, targetType, targetID, outcome, formatTime(at))
	return err
}

// Record implements serverauth.Auditor by writing into the same audit_events
// table appendAuditTx uses, so a mutation or denial the public server core
// reports (object/ref writes, project create/rename, and any Authorize
// denial) lands in the same self-hosted audit trail as the identity routes
// selfhosted handles directly. Recording is a single autocommit insert rather
// than participating in a business transaction, since the core calls this
// after its own mutation (or its own denial) has already been decided.
func (s *identityStore) Record(ctx context.Context, event serverauth.AuditEvent) error {
	at := event.At
	if at.IsZero() {
		at = s.now()
	}
	actorKind := event.ActorKind
	if actorKind == "" {
		actorKind = "user"
	}
	var actor sql.NullString
	if event.Actor != "" {
		actor = sql.NullString{String: event.Actor, Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_events(actor_id, action, target_type, target_id, outcome, created_at, request_id, actor_kind, tenant_id, project_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		actor, string(event.Action), event.TargetType, event.TargetID, event.Outcome, formatTime(at),
		event.RequestID, actorKind, event.TenantID, event.ProjectID)
	return err
}

func validateUserInput(username, displayName string) (string, string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	displayName = strings.TrimSpace(displayName)
	if !usernamePattern.MatchString(username) {
		return "", "", fmt.Errorf("username must be 1-64 lowercase letters, digits, '.', '_' or '-'")
	}
	if displayName == "" || len(displayName) > 120 {
		return "", "", fmt.Errorf("display name must be 1-120 characters")
	}
	if strings.IndexFunc(displayName, unicode.IsControl) >= 0 {
		return "", "", fmt.Errorf("display name must not contain control characters")
	}
	return username, displayName, nil
}

func roleRank(role Role) int {
	switch role {
	case RoleReader:
		return 1
	case RoleWriter:
		return 2
	case RoleAdmin:
		return 3
	case RoleOwner:
		return 4
	default:
		return 0
	}
}

func hasRole(roles []string, want string) bool {
	for _, role := range roles {
		if role == want {
			return true
		}
	}
	return false
}

func newSecret(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate credential: %w", err)
	}
	return prefix + hex.EncodeToString(b), nil
}

func newID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(b), nil
}

func hashSecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

func isUnsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func formatTime(t time.Time) string         { return t.UTC().Format(time.RFC3339Nano) }
func parseTime(v string) (time.Time, error) { return time.Parse(time.RFC3339Nano, v) }
func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
