package selfhosted

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/serverauth"
)

// orgSlugPattern matches the characters slugifyOrgName collapses a display
// name into. Distinct from conformance_test.go's own slugPattern (used to
// derive project ids for fixtures) so the two files, both in package
// selfhosted, never collide on the same identifier.
var orgSlugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// Organization is the public representation of the single self-hosted
// organization record RFC 0005 screen 1 creates.
type Organization struct {
	Slug              string   `json:"slug"`
	DisplayName       string   `json:"display_name"`
	ServerURL         string   `json:"server_url"`
	JoinPolicy        string   `json:"join_policy"`
	DefaultRole       string   `json:"default_role"`
	Onboarding        string   `json:"onboarding"`
	AllowedGitHubOrgs []string `json:"allowed_github_orgs"`

	id string
}

// errSingleOrg is returned by createOrganization once an organization already
// exists: self-hosted supports exactly one, per RFC 0005.
var errSingleOrg = errors.New("single_org")

// errNoOrganization means no organization has been created yet (onboarding
// screen 1 has not been saved).
var errNoOrganization = errors.New("organization not found")

// errOnboardingBackwards means an onboarding state transition tried to move
// somewhere other than forward.
var errOnboardingBackwards = errors.New("onboarding state only moves forward")

// errLastAdmin means an operation would leave the organization with no admin.
var errLastAdmin = errors.New("last_admin")

type orgCreate struct {
	DisplayName string
	Slug        string
	ServerURL   string
	JoinPolicy  string
	DefaultRole string
}

func slugifyOrgName(s string) string {
	slug := orgSlugPattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "org"
	}
	if len(slug) > 60 {
		slug = slug[:60]
	}
	return slug
}

// createOrganization creates the single self-hosted organization row inside
// tx (the caller composes this with the admin-password replacement so the two
// commit atomically, per RFC 0005's "Atomic" requirement on
// POST /api/v1/onboarding/admin).
func createOrganizationTx(tx *sql.Tx, now time.Time, in orgCreate) (Organization, error) {
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM organizations").Scan(&count); err != nil {
		return Organization{}, err
	}
	if count != 0 {
		return Organization{}, errSingleOrg
	}
	slug := in.Slug
	if slug == "" {
		slug = slugifyOrgName(in.DisplayName)
	} else {
		slug = slugifyOrgName(slug)
	}
	if in.JoinPolicy == "" {
		in.JoinPolicy = "invite_only"
	}
	if in.DefaultRole == "" {
		in.DefaultRole = "reader"
	}
	id, err := newID("org")
	if err != nil {
		return Organization{}, err
	}
	if _, err := tx.Exec(`INSERT INTO organizations(id, slug, display_name, server_url, join_policy, default_role, onboarding_state, allowed_github_orgs, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, 'connect', '[]', ?, ?)`,
		id, slug, in.DisplayName, in.ServerURL, in.JoinPolicy, in.DefaultRole, formatTime(now), formatTime(now)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Organization{}, fmt.Errorf("organization slug %q already exists", slug)
		}
		return Organization{}, err
	}
	return Organization{id: id, Slug: slug, DisplayName: in.DisplayName, ServerURL: in.ServerURL,
		JoinPolicy: in.JoinPolicy, DefaultRole: in.DefaultRole, Onboarding: "connect", AllowedGitHubOrgs: []string{}}, nil
}

func scanOrganization(row interface{ Scan(...any) error }) (Organization, error) {
	var org Organization
	var allowed string
	if err := row.Scan(&org.id, &org.Slug, &org.DisplayName, &org.ServerURL, &org.JoinPolicy, &org.DefaultRole, &org.Onboarding, &allowed); err != nil {
		return Organization{}, err
	}
	_ = json.Unmarshal([]byte(allowed), &org.AllowedGitHubOrgs)
	if org.AllowedGitHubOrgs == nil {
		org.AllowedGitHubOrgs = []string{}
	}
	return org, nil
}

const orgColumns = `id, slug, display_name, server_url, join_policy, default_role, onboarding_state, allowed_github_orgs`

// getOrganization returns the single organization. errNoOrganization when
// screen 1 has not been saved yet.
func (s *identityStore) getOrganization() (Organization, error) {
	row := s.db.QueryRow(`SELECT ` + orgColumns + ` FROM organizations LIMIT 1`)
	org, err := scanOrganization(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Organization{}, errNoOrganization
	}
	return org, err
}

func (s *identityStore) getOrganizationBySlug(slug string) (Organization, error) {
	row := s.db.QueryRow(`SELECT `+orgColumns+` FROM organizations WHERE slug=? COLLATE NOCASE`, slug)
	org, err := scanOrganization(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Organization{}, errNoOrganization
	}
	return org, err
}

type orgPatch struct {
	DisplayName       *string
	ServerURL         *string
	JoinPolicy        *string
	DefaultRole       *string
	AllowedGitHubOrgs *[]string
	Slug              *string
}

func (s *identityStore) updateOrganization(actorID, slug string, patch orgPatch) (Organization, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Organization{}, err
	}
	defer func() { _ = tx.Rollback() }()
	org, err := func() (Organization, error) {
		row := tx.QueryRow(`SELECT `+orgColumns+` FROM organizations WHERE slug=? COLLATE NOCASE`, slug)
		return scanOrganization(row)
	}()
	if errors.Is(err, sql.ErrNoRows) {
		return Organization{}, errNoOrganization
	}
	if err != nil {
		return Organization{}, err
	}
	if patch.DisplayName != nil {
		org.DisplayName = *patch.DisplayName
	}
	if patch.ServerURL != nil {
		org.ServerURL = *patch.ServerURL
	}
	if patch.JoinPolicy != nil {
		if *patch.JoinPolicy != "invite_only" && *patch.JoinPolicy != "open" {
			return Organization{}, fmt.Errorf("invalid join_policy %q", *patch.JoinPolicy)
		}
		org.JoinPolicy = *patch.JoinPolicy
	}
	if patch.DefaultRole != nil {
		if roleRank(Role(*patch.DefaultRole)) == 0 {
			return Organization{}, fmt.Errorf("invalid default_role %q", *patch.DefaultRole)
		}
		org.DefaultRole = *patch.DefaultRole
	}
	if patch.AllowedGitHubOrgs != nil {
		org.AllowedGitHubOrgs = *patch.AllowedGitHubOrgs
	}
	newSlug := org.Slug
	if patch.Slug != nil && *patch.Slug != "" {
		newSlug = slugifyOrgName(*patch.Slug)
	}
	allowed, _ := json.Marshal(org.AllowedGitHubOrgs)
	now := s.now()
	if _, err := tx.Exec(`UPDATE organizations SET slug=?, display_name=?, server_url=?, join_policy=?, default_role=?, allowed_github_orgs=?, updated_at=? WHERE id=?`,
		newSlug, org.DisplayName, org.ServerURL, org.JoinPolicy, org.DefaultRole, string(allowed), formatTime(now), org.id); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Organization{}, fmt.Errorf("organization slug %q already exists", newSlug)
		}
		return Organization{}, err
	}
	org.Slug = newSlug
	if err := appendAuditTx(tx, actorID, "organization.update", "organization", org.id, "success", now); err != nil {
		return Organization{}, err
	}
	if err := tx.Commit(); err != nil {
		return Organization{}, err
	}
	return org, nil
}

var onboardingOrder = map[string]int{"connect": 0, "users": 1, "done": 2}

// advanceOnboarding moves the organization's onboarding state forward only,
// per Appendix A ("only forward moves, connect -> users -> done").
func (s *identityStore) advanceOnboarding(actorID, slug, newState string) (Organization, error) {
	rank, ok := onboardingOrder[newState]
	if !ok {
		return Organization{}, fmt.Errorf("invalid onboarding state %q", newState)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Organization{}, err
	}
	defer func() { _ = tx.Rollback() }()
	org, err := func() (Organization, error) {
		row := tx.QueryRow(`SELECT `+orgColumns+` FROM organizations WHERE slug=? COLLATE NOCASE`, slug)
		return scanOrganization(row)
	}()
	if errors.Is(err, sql.ErrNoRows) {
		return Organization{}, errNoOrganization
	}
	if err != nil {
		return Organization{}, err
	}
	if rank < onboardingOrder[org.Onboarding] {
		return Organization{}, errOnboardingBackwards
	}
	now := s.now()
	if _, err := tx.Exec(`UPDATE organizations SET onboarding_state=?, updated_at=? WHERE id=?`, newState, formatTime(now), org.id); err != nil {
		return Organization{}, err
	}
	org.Onboarding = newState
	if err := appendAuditTx(tx, actorID, "organization.onboarding", "organization", org.id, "success", now); err != nil {
		return Organization{}, err
	}
	if err := tx.Commit(); err != nil {
		return Organization{}, err
	}
	return org, nil
}

// ---- password authentication -------------------------------------------

// verifyLogin checks username/password and reports the matching user. It
// runs the Argon2id comparison even when the username does not exist (against
// a fixed dummy hash) so that a login attempt against an unknown username and
// one against a known username with a wrong password take comparable time.
// Every failure is audited (action "login.failure", target the attempted
// username, never the password) so repeated guessing shows up in the audit
// trail even though it is also rate limited.
func (s *identityStore) verifyLogin(username, password string) (User, error) {
	var user User
	var instanceOwner, passwordChangeRequired int
	var createdAt, hash string
	err := s.db.QueryRow(`SELECT u.id, u.username, u.display_name, u.email, u.instance_owner, u.org_role, u.password_change_required, u.created_at, p.hash
FROM users u JOIN passwords p ON p.user_id=u.id
WHERE u.username=? COLLATE NOCASE AND u.disabled_at IS NULL`, username).
		Scan(&user.ID, &user.Username, &user.DisplayName, &user.Email, &instanceOwner, &user.OrgRole, &passwordChangeRequired, &createdAt, &hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Burn comparable time to a real verification so username
			// enumeration cannot be timed.
			_, _ = verifyPassword(password, dummyPasswordHash)
			s.auditLoginFailure(username)
			return User{}, serverauth.ErrUnauthenticated
		}
		return User{}, err
	}
	ok, err := verifyPassword(password, hash)
	if err != nil {
		return User{}, err
	}
	if !ok {
		s.auditLoginFailure(username)
		return User{}, serverauth.ErrUnauthenticated
	}
	user.CreatedAt, _ = parseTime(createdAt)
	user.InstanceOwner = instanceOwner == 1
	return user, nil
}

// auditLoginFailure records a failed login attempt as a standalone (non-
// transactional) insert: there is no wider business transaction to join
// here, and a best-effort audit row must never turn a rejected login into a
// 500. Errors are deliberately swallowed for the same reason.
func (s *identityStore) auditLoginFailure(username string) {
	_, _ = s.db.Exec(`INSERT INTO audit_events(actor_id, action, target_type, target_id, outcome, created_at, actor_kind)
VALUES (NULL, 'login.failure', 'user', ?, 'denied', ?, 'anonymous')`, username, formatTime(s.now()))
}

// dummyPasswordHash is a real Argon2id hash of a fixed, never-used password,
// computed once so verifyLogin can spend the same work on an unknown username
// as on a known one.
var dummyPasswordHash = mustHashPassword("re-gent-timing-decoy-password")

func mustHashPassword(pw string) string {
	h, err := hashPassword(pw)
	if err != nil {
		panic(err)
	}
	return h
}

// changePassword verifies current (unless skipCurrent) and replaces the
// stored hash, clearing password_change_required.
func (s *identityStore) changePassword(userID, current, newPassword string, skipCurrentCheck bool) error {
	if len(newPassword) < 12 {
		return errPasswordTooShort
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if !skipCurrentCheck {
		var hash string
		if err := tx.QueryRow("SELECT hash FROM passwords WHERE user_id=?", userID).Scan(&hash); err != nil {
			return err
		}
		ok, err := verifyPassword(current, hash)
		if err != nil {
			return err
		}
		if !ok {
			return serverauth.ErrUnauthenticated
		}
	}
	newHash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	now := s.now()
	if _, err := tx.Exec(`INSERT INTO passwords(user_id, hash, created_at, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT(user_id) DO UPDATE SET hash=excluded.hash, updated_at=excluded.updated_at`, userID, newHash, formatTime(now), formatTime(now)); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE users SET password_change_required=0 WHERE id=?`, userID); err != nil {
		return err
	}
	if err := appendAuditTx(tx, userID, "password.change", "user", userID, "success", now); err != nil {
		return err
	}
	return tx.Commit()
}

var errPasswordTooShort = errors.New("password_too_short")

// userPasswordChangeRequired reports whether userID must still replace its
// initial password before using a non-auth route.
func (s *identityStore) userPasswordChangeRequired(userID string) (bool, error) {
	var required int
	if err := s.db.QueryRow(`SELECT password_change_required FROM users WHERE id=?`, userID).Scan(&required); err != nil {
		return false, err
	}
	return required == 1, nil
}

// recentTokenName returns the name of userID's most recently used PAT, used
// by the connections feed to show a human-readable machine name (setup-code
// exchange names a PAT "<machine_name> (setup)"). "" when the user has no
// PAT at all (a browser-only session, for instance).
func (s *identityStore) recentTokenName(userID string) (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM credentials WHERE user_id=? AND kind='pat' AND revoked_at IS NULL
ORDER BY last_used_at DESC, created_at DESC LIMIT 1`, userID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// completeOnboardingAdmin is the atomic operation behind
// POST /api/v1/onboarding/admin: it replaces the admin's password, optionally
// renames the admin and updates display name/email, creates the single
// organization, and moves the instance out of the admin_password state, all
// inside one transaction.
type onboardingAdminRequest struct {
	ActorUserID     string // "" when authenticating via CurrentPassword instead of a session
	CurrentUsername string
	CurrentPassword string
	NewUsername     string
	DisplayName     string
	Email           string
	NewPassword     string
	Org             orgCreate
}

func (s *identityStore) completeOnboardingAdmin(req onboardingAdminRequest) (User, Organization, error) {
	if len(req.NewPassword) < 12 {
		return User{}, Organization{}, errPasswordTooShort
	}
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, Organization{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var userID, username, hash string
	var passwordChangeRequired int
	if req.ActorUserID != "" {
		if err := tx.QueryRow(`SELECT id, username, password_change_required FROM users WHERE id=? AND instance_owner=1`, req.ActorUserID).
			Scan(&userID, &username, &passwordChangeRequired); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return User{}, Organization{}, serverauth.ErrForbidden
			}
			return User{}, Organization{}, err
		}
	} else {
		if err := tx.QueryRow(`SELECT id, username, password_change_required FROM users WHERE username=? COLLATE NOCASE AND instance_owner=1`, req.CurrentUsername).
			Scan(&userID, &username, &passwordChangeRequired); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return User{}, Organization{}, serverauth.ErrUnauthenticated
			}
			return User{}, Organization{}, err
		}
		if passwordChangeRequired != 1 {
			return User{}, Organization{}, serverauth.ErrForbidden
		}
		if err := tx.QueryRow("SELECT hash FROM passwords WHERE user_id=?", userID).Scan(&hash); err != nil {
			return User{}, Organization{}, err
		}
		ok, err := verifyPassword(req.CurrentPassword, hash)
		if err != nil {
			return User{}, Organization{}, err
		}
		if !ok {
			return User{}, Organization{}, serverauth.ErrUnauthenticated
		}
	}

	newUsername := username
	if req.NewUsername != "" {
		validated, _, verr := validateUserInput(req.NewUsername, req.DisplayName)
		if verr != nil {
			return User{}, Organization{}, verr
		}
		newUsername = validated
	}
	displayName := req.DisplayName
	if displayName == "" {
		displayName = newUsername
	}
	now := s.now()
	newHash, err := hashPassword(req.NewPassword)
	if err != nil {
		return User{}, Organization{}, err
	}
	if _, err := tx.Exec(`UPDATE users SET username=?, display_name=?, email=?, password_change_required=0 WHERE id=?`,
		newUsername, displayName, req.Email, userID); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return User{}, Organization{}, fmt.Errorf("username %q already exists", newUsername)
		}
		return User{}, Organization{}, err
	}
	if _, err := tx.Exec(`INSERT INTO passwords(user_id, hash, created_at, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT(user_id) DO UPDATE SET hash=excluded.hash, updated_at=excluded.updated_at`, userID, newHash, formatTime(now), formatTime(now)); err != nil {
		return User{}, Organization{}, err
	}
	org, err := createOrganizationTx(tx, now, req.Org)
	if err != nil {
		return User{}, Organization{}, err
	}
	if err := appendAuditTx(tx, userID, "onboarding.admin", "user", userID, "success", now); err != nil {
		return User{}, Organization{}, err
	}
	if err := appendAuditTx(tx, userID, "organization.create", "organization", org.id, "success", now); err != nil {
		return User{}, Organization{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, Organization{}, err
	}
	user := User{ID: userID, Username: newUsername, DisplayName: displayName, Email: req.Email, InstanceOwner: true, OrgRole: "admin", CreatedAt: now}
	return user, org, nil
}

// ---- setup codes ---------------------------------------------------------

func (s *identityStore) createSetupCode(orgID, userID string) (string, time.Time, error) {
	code, err := newSetupCode()
	if err != nil {
		return "", time.Time{}, err
	}
	id, err := newID("stc")
	if err != nil {
		return "", time.Time{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", time.Time{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	expires := now.Add(setupCodeLifetime)
	if _, err := tx.Exec(`INSERT INTO setup_codes(id, org_id, user_id, code_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, orgID, userID, hashSecret(code), formatTime(now), formatTime(expires)); err != nil {
		return "", time.Time{}, err
	}
	if err := appendAuditTx(tx, userID, "setup_code.create", "setup_code", id, "success", now); err != nil {
		return "", time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, err
	}
	return code, expires, nil
}

var errSetupCodeInvalid = errors.New("setup_code_invalid")
var errSetupCodeExpired = errors.New("setup_code_expired")

// exchangeSetupCode consumes a one-time setup code and mints a machine PAT
// for the bound user, identical in shape to any other PAT.
func (s *identityStore) exchangeSetupCode(code, machineName string) (User, Organization, string, time.Time, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, Organization{}, "", time.Time{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var id, orgID, userID, expiresAt string
	var usedAt sql.NullString
	err = tx.QueryRow(`SELECT id, org_id, user_id, expires_at, used_at FROM setup_codes WHERE code_hash=?`, hashSecret(code)).
		Scan(&id, &orgID, &userID, &expiresAt, &usedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, Organization{}, "", time.Time{}, errSetupCodeInvalid
		}
		return User{}, Organization{}, "", time.Time{}, err
	}
	if usedAt.Valid {
		return User{}, Organization{}, "", time.Time{}, errSetupCodeInvalid
	}
	expires, err := parseTime(expiresAt)
	now := s.now()
	if err != nil || !expires.After(now) {
		return User{}, Organization{}, "", time.Time{}, errSetupCodeExpired
	}
	if _, err := tx.Exec(`UPDATE setup_codes SET used_at=? WHERE id=?`, formatTime(now), id); err != nil {
		return User{}, Organization{}, "", time.Time{}, err
	}
	var user User
	var instanceOwner int
	var createdAt string
	if err := tx.QueryRow(`SELECT id, username, display_name, email, instance_owner, org_role, created_at FROM users WHERE id=?`, userID).
		Scan(&user.ID, &user.Username, &user.DisplayName, &user.Email, &instanceOwner, &user.OrgRole, &createdAt); err != nil {
		return User{}, Organization{}, "", time.Time{}, err
	}
	user.InstanceOwner = instanceOwner == 1
	user.CreatedAt, _ = parseTime(createdAt)
	org, err := func() (Organization, error) {
		row := tx.QueryRow(`SELECT `+orgColumns+` FROM organizations WHERE id=?`, orgID)
		return scanOrganization(row)
	}()
	if err != nil {
		return User{}, Organization{}, "", time.Time{}, err
	}
	name := strings.TrimSpace(machineName)
	if name == "" {
		name = "machine"
	}
	name += " (setup)"
	secret, _, _, err := issueCredentialTx(tx, userID, "pat", name, now, now.Add(defaultPATLifetime))
	if err != nil {
		return User{}, Organization{}, "", time.Time{}, err
	}
	if err := appendAuditTx(tx, userID, "setup_code.use", "setup_code", id, "success", now); err != nil {
		return User{}, Organization{}, "", time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, Organization{}, "", time.Time{}, err
	}
	return user, org, secret, now.Add(defaultPATLifetime), nil
}

// ---- connections feed ------------------------------------------------

// Connection is one row of the wizard's live enrollment feed.
type Connection struct {
	ProjectID   string `json:"project_id"`
	DisplayName string `json:"display_name"`
	Remote      string `json:"remote"`
	MachineName string `json:"machine_name"`
	ConnectedBy string `json:"connected_by"`
	ConnectedAt string `json:"connected_at"`
	seq         int64
}

func (s *identityStore) recordConnection(orgID, projectID, displayName, remote, machineName, connectedBy string) error {
	now := formatTime(s.now())
	_, err := s.db.Exec(`INSERT INTO connections(org_id, project_id, display_name, remote, machine_name, connected_by, connected_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, orgID, projectID, displayName, remote, machineName, connectedBy, now)
	return err
}

func (s *identityStore) listConnectionsSince(orgID string, cursor int64) ([]Connection, int64, error) {
	rows, err := s.db.Query(`SELECT seq, project_id, display_name, remote, machine_name, connected_by, connected_at
FROM connections WHERE org_id=? AND seq>? ORDER BY seq`, orgID, cursor)
	if err != nil {
		return nil, cursor, err
	}
	defer rows.Close()
	out := []Connection{}
	latest := cursor
	for rows.Next() {
		var c Connection
		if err := rows.Scan(&c.seq, &c.ProjectID, &c.DisplayName, &c.Remote, &c.MachineName, &c.ConnectedBy, &c.ConnectedAt); err != nil {
			return nil, cursor, err
		}
		if c.seq > latest {
			latest = c.seq
		}
		out = append(out, c)
	}
	return out, latest, rows.Err()
}

// ---- organization members -------------------------------------------

// OrgMember is one row of GET /api/v1/orgs/{slug}/members.
type OrgMember struct {
	UserID      string   `json:"user_id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	Role        string   `json:"role"`
	Methods     []string `json:"methods"`
}

func (s *identityStore) listOrgMembers() ([]OrgMember, error) {
	rows, err := s.db.Query(`SELECT u.id, u.username, u.display_name, u.org_role,
CASE WHEN p.user_id IS NOT NULL THEN 1 ELSE 0 END
FROM users u LEFT JOIN passwords p ON p.user_id=u.id
WHERE u.disabled_at IS NULL ORDER BY u.username COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OrgMember{}
	for rows.Next() {
		var m OrgMember
		var hasPassword int
		if err := rows.Scan(&m.UserID, &m.Username, &m.DisplayName, &m.Role, &hasPassword); err != nil {
			return nil, err
		}
		if hasPassword == 1 {
			m.Methods = []string{"password"}
		} else {
			m.Methods = []string{}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *identityStore) setOrgRole(actorID, userID, role string) error {
	if role != "admin" && role != "member" {
		return fmt.Errorf("invalid role %q", role)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var currentRole string
	if err := tx.QueryRow(`SELECT org_role FROM users WHERE id=? AND disabled_at IS NULL`, userID).Scan(&currentRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return serverauth.ErrNotFound
		}
		return err
	}
	if currentRole == "admin" && role != "admin" {
		var adminCount int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE org_role='admin' AND disabled_at IS NULL`).Scan(&adminCount); err != nil {
			return err
		}
		if adminCount <= 1 {
			return errLastAdmin
		}
	}
	if _, err := tx.Exec(`UPDATE users SET org_role=? WHERE id=?`, role, userID); err != nil {
		return err
	}
	if err := appendAuditTx(tx, actorID, "member.role_change", "user", userID, "success", s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- auth method settings (GitHub/Google/SMTP) -------------------------

// AuthMethodSettings is the redacted wire shape of
// GET /api/v1/orgs/{slug}/auth-methods: secrets never appear, only
// has_secret.
type AuthMethodSettings struct {
	Password struct {
		Enabled bool `json:"enabled"`
	} `json:"password"`
	GitHub struct {
		Enabled     bool   `json:"enabled"`
		ClientID    string `json:"client_id,omitempty"`
		BaseURL     string `json:"base_url,omitempty"`
		HasSecret   bool   `json:"has_secret"`
		CallbackURL string `json:"callback_url,omitempty"`
	} `json:"github"`
	Google struct {
		Enabled   bool   `json:"enabled"`
		ClientID  string `json:"client_id,omitempty"`
		HasSecret bool   `json:"has_secret"`
	} `json:"google"`
	SMTP struct {
		Enabled     bool   `json:"enabled"`
		Host        string `json:"host,omitempty"`
		Port        int    `json:"port,omitempty"`
		Username    string `json:"username,omitempty"`
		From        string `json:"from,omitempty"`
		HasPassword bool   `json:"has_password"`
	} `json:"smtp"`
}

func (s *identityStore) getAuthMethodSettings(orgID, serverURL string) (AuthMethodSettings, error) {
	var out AuthMethodSettings
	out.Password.Enabled = true
	var githubEnabled, googleEnabled, smtpEnabled int
	var githubClientID, githubBaseURL, googleClientID, smtpHost, smtpUsername, smtpFrom string
	var smtpPort int
	var githubSecret, googleSecret, smtpPassword []byte
	err := s.db.QueryRow(`SELECT github_enabled, github_client_id, github_client_secret_enc, github_base_url,
google_enabled, google_client_id, google_client_secret_enc,
smtp_enabled, smtp_host, smtp_port, smtp_username, smtp_password_enc, smtp_from
FROM auth_method_settings WHERE org_id=?`, orgID).Scan(
		&githubEnabled, &githubClientID, &githubSecret, &githubBaseURL,
		&googleEnabled, &googleClientID, &googleSecret,
		&smtpEnabled, &smtpHost, &smtpPort, &smtpUsername, &smtpPassword, &smtpFrom)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.GitHub.Enabled = githubEnabled == 1
	out.GitHub.ClientID = githubClientID
	out.GitHub.BaseURL = githubBaseURL
	out.GitHub.HasSecret = len(githubSecret) > 0
	if out.GitHub.Enabled {
		out.GitHub.CallbackURL = strings.TrimRight(serverURL, "/") + "/api/v1/auth/github/callback"
	}
	out.Google.Enabled = googleEnabled == 1
	out.Google.ClientID = googleClientID
	out.Google.HasSecret = len(googleSecret) > 0
	out.SMTP.Enabled = smtpEnabled == 1
	out.SMTP.Host = smtpHost
	out.SMTP.Port = smtpPort
	out.SMTP.Username = smtpUsername
	out.SMTP.From = smtpFrom
	out.SMTP.HasPassword = len(smtpPassword) > 0
	return out, nil
}

// authMethodPatch is the request shape of PUT /api/v1/orgs/{slug}/auth-methods.
// A nil *string secret field keeps the stored one; an empty non-nil string
// clears it.
type authMethodPatch struct {
	GitHubEnabled      *bool
	GitHubClientID     *string
	GitHubClientSecret *string
	GitHubBaseURL      *string
	GoogleEnabled      *bool
	GoogleClientID     *string
	GoogleClientSecret *string
	SMTPEnabled        *bool
	SMTPHost           *string
	SMTPPort           *int
	SMTPUsername       *string
	SMTPPassword       *string
	SMTPFrom           *string
}

func (s *identityStore) putAuthMethodSettings(actorID, orgID string, patch authMethodPatch, box *secretBox) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var githubEnabled, googleEnabled, smtpEnabled int
	var githubClientID, githubBaseURL, googleClientID, smtpHost, smtpUsername, smtpFrom string
	var smtpPort int
	var githubSecret, googleSecret, smtpPassword []byte
	err = tx.QueryRow(`SELECT github_enabled, github_client_id, github_client_secret_enc, github_base_url,
google_enabled, google_client_id, google_client_secret_enc,
smtp_enabled, smtp_host, smtp_port, smtp_username, smtp_password_enc, smtp_from
FROM auth_method_settings WHERE org_id=?`, orgID).Scan(
		&githubEnabled, &githubClientID, &githubSecret, &githubBaseURL,
		&googleEnabled, &googleClientID, &googleSecret,
		&smtpEnabled, &smtpHost, &smtpPort, &smtpUsername, &smtpPassword, &smtpFrom)
	exists := true
	if errors.Is(err, sql.ErrNoRows) {
		exists = false
	} else if err != nil {
		return err
	}

	if patch.GitHubEnabled != nil {
		githubEnabled = boolToInt(*patch.GitHubEnabled)
	}
	if patch.GitHubClientID != nil {
		githubClientID = *patch.GitHubClientID
	}
	if patch.GitHubBaseURL != nil {
		githubBaseURL = *patch.GitHubBaseURL
	}
	if patch.GitHubClientSecret != nil {
		if *patch.GitHubClientSecret == "" {
			githubSecret = nil
		} else if githubSecret, err = box.seal(*patch.GitHubClientSecret); err != nil {
			return err
		}
	}
	if patch.GoogleEnabled != nil {
		googleEnabled = boolToInt(*patch.GoogleEnabled)
	}
	if patch.GoogleClientID != nil {
		googleClientID = *patch.GoogleClientID
	}
	if patch.GoogleClientSecret != nil {
		if *patch.GoogleClientSecret == "" {
			googleSecret = nil
		} else if googleSecret, err = box.seal(*patch.GoogleClientSecret); err != nil {
			return err
		}
	}
	if patch.SMTPEnabled != nil {
		smtpEnabled = boolToInt(*patch.SMTPEnabled)
	}
	if patch.SMTPHost != nil {
		smtpHost = *patch.SMTPHost
	}
	if patch.SMTPPort != nil {
		smtpPort = *patch.SMTPPort
	}
	if patch.SMTPUsername != nil {
		smtpUsername = *patch.SMTPUsername
	}
	if patch.SMTPFrom != nil {
		smtpFrom = *patch.SMTPFrom
	}
	if patch.SMTPPassword != nil {
		if *patch.SMTPPassword == "" {
			smtpPassword = nil
		} else if smtpPassword, err = box.seal(*patch.SMTPPassword); err != nil {
			return err
		}
	}

	now := formatTime(s.now())
	if exists {
		if _, err := tx.Exec(`UPDATE auth_method_settings SET github_enabled=?, github_client_id=?, github_client_secret_enc=?, github_base_url=?,
google_enabled=?, google_client_id=?, google_client_secret_enc=?,
smtp_enabled=?, smtp_host=?, smtp_port=?, smtp_username=?, smtp_password_enc=?, smtp_from=?, updated_at=?
WHERE org_id=?`, githubEnabled, githubClientID, githubSecret, githubBaseURL,
			googleEnabled, googleClientID, googleSecret,
			smtpEnabled, smtpHost, smtpPort, smtpUsername, smtpPassword, smtpFrom, now, orgID); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`INSERT INTO auth_method_settings(org_id, github_enabled, github_client_id, github_client_secret_enc, github_base_url,
google_enabled, google_client_id, google_client_secret_enc,
smtp_enabled, smtp_host, smtp_port, smtp_username, smtp_password_enc, smtp_from, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, orgID, githubEnabled, githubClientID, githubSecret, githubBaseURL,
			googleEnabled, googleClientID, googleSecret,
			smtpEnabled, smtpHost, smtpPort, smtpUsername, smtpPassword, smtpFrom, now); err != nil {
			return err
		}
	}
	if err := appendAuditTx(tx, actorID, "auth_methods.update", "organization", orgID, "success", s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
