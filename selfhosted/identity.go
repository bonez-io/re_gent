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
	bootstrapPrefix     = "rgt_bootstrap_"
	defaultPATLifetime  = 30 * 24 * time.Hour
	sessionLifetime     = 12 * time.Hour
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
	InstanceOwner bool      `json:"instance_owner"`
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
	principal serverauth.Principal
	user      User
	tokenID   string
	tokenKind string
	csrf      string
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
CREATE TABLE IF NOT EXISTS bootstrap (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  secret_hash BLOB NOT NULL,
  created_at TEXT NOT NULL
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
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("create identity schema: %w", err)
	}
	return nil
}

func (s *identityStore) close() error { return s.db.Close() }

// rotateBootstrap returns a fresh one-time bootstrap credential while no user
// exists. Restarting an unclaimed server invalidates the credential printed by
// the previous process, so a lost operator terminal never leaves an unknown
// live secret behind.
func (s *identityStore) rotateBootstrap() (string, bool, error) {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return "", false, fmt.Errorf("count users: %w", err)
	}
	if count != 0 {
		if _, err := s.db.Exec("DELETE FROM bootstrap"); err != nil {
			return "", false, fmt.Errorf("remove stale bootstrap credential: %w", err)
		}
		return "", false, nil
	}
	secret, err := newSecret(bootstrapPrefix)
	if err != nil {
		return "", false, err
	}
	_, err = s.db.Exec(`INSERT INTO bootstrap(singleton, secret_hash, created_at)
VALUES (1, ?, ?) ON CONFLICT(singleton) DO UPDATE SET secret_hash=excluded.secret_hash, created_at=excluded.created_at`,
		hashSecret(secret), formatTime(s.now()))
	if err != nil {
		return "", false, fmt.Errorf("store bootstrap credential: %w", err)
	}
	return secret, true, nil
}

func (s *identityStore) bootstrapRequired() (bool, error) {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return false, fmt.Errorf("count users: %w", err)
	}
	return count == 0, nil
}

func (s *identityStore) checkBootstrap(secret string) error {
	if !strings.HasPrefix(secret, bootstrapPrefix) {
		return serverauth.ErrUnauthenticated
	}
	var want []byte
	if err := s.db.QueryRow("SELECT secret_hash FROM bootstrap WHERE singleton=1").Scan(&want); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return serverauth.ErrUnauthenticated
		}
		return err
	}
	got := hashSecret(secret)
	if len(want) != len(got) || subtle.ConstantTimeCompare(want, got) != 1 {
		return serverauth.ErrUnauthenticated
	}
	return nil
}

func (s *identityStore) createFirstOwner(username, displayName string) (User, string, string, string, error) {
	username, displayName, err := validateUserInput(username, displayName)
	if err != nil {
		return User{}, "", "", "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, "", "", "", err
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return User{}, "", "", "", err
	}
	if count != 0 {
		return User{}, "", "", "", fmt.Errorf("bootstrap is already complete")
	}
	now := s.now()
	userID, err := newID("usr")
	if err != nil {
		return User{}, "", "", "", err
	}
	user := User{ID: userID, Username: username, DisplayName: displayName, InstanceOwner: true, CreatedAt: now}
	if _, err := tx.Exec(`INSERT INTO users(id, username, display_name, instance_owner, created_at) VALUES (?, ?, ?, 1, ?)`,
		user.ID, user.Username, user.DisplayName, formatTime(now)); err != nil {
		return User{}, "", "", "", fmt.Errorf("create first owner: %w", err)
	}
	pat, _, _, err := issueCredentialTx(tx, user.ID, "pat", "initial setup", now, now.Add(defaultPATLifetime))
	if err != nil {
		return User{}, "", "", "", err
	}
	session, _, csrf, err := issueCredentialTx(tx, user.ID, "session", "browser session", now, now.Add(sessionLifetime))
	if err != nil {
		return User{}, "", "", "", err
	}
	if _, err := tx.Exec("DELETE FROM bootstrap WHERE singleton=1"); err != nil {
		return User{}, "", "", "", err
	}
	if err := appendAuditTx(tx, user.ID, "bootstrap.complete", "user", user.ID, "success", now); err != nil {
		return User{}, "", "", "", err
	}
	if err := tx.Commit(); err != nil {
		return User{}, "", "", "", err
	}
	return user, pat, session, csrf, nil
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
		return authenticated{}, serverauth.ErrUnauthenticated
	}
	if (wantKind == "pat" && !strings.HasPrefix(secret, personalTokenPrefix)) ||
		(wantKind == "session" && !strings.HasPrefix(secret, sessionTokenPrefix)) {
		return authenticated{}, serverauth.ErrUnauthenticated
	}

	var auth authenticated
	var instanceOwner int
	var createdAt, expiresAt string
	var csrf sql.NullString
	err := s.db.QueryRow(`SELECT c.id, c.kind, c.csrf_token,
u.id, u.username, u.display_name, u.instance_owner, u.created_at, c.expires_at
FROM credentials c JOIN users u ON u.id=c.user_id
WHERE c.secret_hash=? AND c.kind=? AND c.revoked_at IS NULL AND u.disabled_at IS NULL`,
		hashSecret(secret), wantKind).Scan(&auth.tokenID, &auth.tokenKind, &csrf,
		&auth.user.ID, &auth.user.Username, &auth.user.DisplayName, &instanceOwner, &createdAt, &expiresAt)
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
	auth.principal = serverauth.Principal{Subject: auth.user.ID, Roles: roles, AuthMethod: authMethod}
	_, _ = s.db.Exec("UPDATE credentials SET last_used_at=? WHERE id=?", formatTime(s.now()), auth.tokenID)
	return auth, nil
}

func (s *identityStore) Authorize(_ context.Context, principal serverauth.Principal, permission serverauth.Permission) error {
	if principal.Subject == "" {
		return serverauth.ErrUnauthenticated
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
	case serverauth.ActionMemberWrite:
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

func appendAuditTx(tx *sql.Tx, actor, action, targetType, targetID, outcome string, at time.Time) error {
	_, err := tx.Exec(`INSERT INTO audit_events(actor_id, action, target_type, target_id, outcome, created_at) VALUES (?, ?, ?, ?, ?, ?)`, actor, action, targetType, targetID, outcome, formatTime(at))
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
