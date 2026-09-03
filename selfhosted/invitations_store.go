package selfhosted

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/smtp"
	"strings"
	"time"
)

// Grant is one project the invitee will hold a role on once they accept.
type Grant struct {
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
}

// Invitation is the public representation of one invitation row.
type Invitation struct {
	ID        string    `json:"id"`
	Email     string    `json:"email,omitempty"`
	Username  string    `json:"username,omitempty"`
	OrgRole   string    `json:"org_role"`
	Grants    []Grant   `json:"grants"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

var (
	errInvitationExpired  = errors.New("invitation_expired")
	errInvitationRevoked  = errors.New("invitation_revoked")
	errInvitationNotFound = errors.New("invitation not found")
)

type invitationCreate struct {
	Email    string
	Username string
	OrgRole  string
	Grants   []Grant
}

func newInvitationToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate invitation token: %w", err)
	}
	return "rgt_invite_" + hex.EncodeToString(b), nil
}

func (s *identityStore) createInvitation(orgID, actorID string, in invitationCreate) (Invitation, string, error) {
	if (in.Email == "") == (in.Username == "") {
		return Invitation{}, "", fmt.Errorf("exactly one of email or username is required")
	}
	if in.OrgRole != "admin" && in.OrgRole != "member" {
		return Invitation{}, "", fmt.Errorf("invalid org_role %q", in.OrgRole)
	}
	grants, err := json.Marshal(in.Grants)
	if err != nil {
		return Invitation{}, "", err
	}
	token, err := newInvitationToken()
	if err != nil {
		return Invitation{}, "", err
	}
	id, err := newID("inv")
	if err != nil {
		return Invitation{}, "", err
	}
	now := s.now()
	expires := now.Add(invitationLifetime)
	tx, err := s.db.Begin()
	if err != nil {
		return Invitation{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO invitations(id, org_id, email, username, org_role, grants, token_hash, status, invited_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)`,
		id, orgID, in.Email, in.Username, in.OrgRole, string(grants), hashSecret(token), actorID, formatTime(now), formatTime(expires)); err != nil {
		return Invitation{}, "", err
	}
	if err := appendAuditTx(tx, actorID, "invitation.create", "invitation", id, "success", now); err != nil {
		return Invitation{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return Invitation{}, "", err
	}
	return Invitation{ID: id, Email: in.Email, Username: in.Username, OrgRole: in.OrgRole, Grants: in.Grants,
		Status: "pending", CreatedAt: now, ExpiresAt: expires}, token, nil
}

func (s *identityStore) listInvitations(orgID string) ([]Invitation, error) {
	rows, err := s.db.Query(`SELECT id, email, username, org_role, grants, status, created_at, expires_at
FROM invitations WHERE org_id=? ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	now := s.now()
	for rows.Next() {
		var inv Invitation
		var grantsJSON, createdAt, expiresAt string
		if err := rows.Scan(&inv.ID, &inv.Email, &inv.Username, &inv.OrgRole, &grantsJSON, &inv.Status, &createdAt, &expiresAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(grantsJSON), &inv.Grants)
		inv.CreatedAt, _ = parseTime(createdAt)
		inv.ExpiresAt, _ = parseTime(expiresAt)
		if inv.Status == "pending" && !inv.ExpiresAt.After(now) {
			inv.Status = "expired"
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *identityStore) revokeInvitation(actorID, orgID, id string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE invitations SET status='revoked', revoked_at=? WHERE id=? AND org_id=? AND status='pending'`,
		formatTime(s.now()), id, orgID)
	if err != nil {
		return false, err
	}
	count, _ := res.RowsAffected()
	if count == 0 {
		return false, nil
	}
	if err := appendAuditTx(tx, actorID, "invitation.revoke", "invitation", id, "success", s.now()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// invitationPublic is the public GET /api/v1/invitations/{token} shape.
type invitationPublic struct {
	OrgDisplayName string   `json:"org_display_name"`
	Email          string   `json:"email,omitempty"`
	Username       string   `json:"username,omitempty"`
	Methods        []string `json:"methods"`
}

func (s *identityStore) getInvitationByToken(token string) (invitationPublic, error) {
	var email, username, status, expiresAt, orgID string
	err := s.db.QueryRow(`SELECT email, username, status, expires_at, org_id FROM invitations WHERE token_hash=?`, hashSecret(token)).
		Scan(&email, &username, &status, &expiresAt, &orgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return invitationPublic{}, errInvitationNotFound
		}
		return invitationPublic{}, err
	}
	if status == "revoked" {
		return invitationPublic{}, errInvitationRevoked
	}
	expires, _ := parseTime(expiresAt)
	if status == "accepted" || (status == "pending" && !expires.After(s.now())) {
		if status == "pending" {
			return invitationPublic{}, errInvitationExpired
		}
		return invitationPublic{}, errInvitationNotFound
	}
	var org Organization
	row := s.db.QueryRow(`SELECT `+orgColumns+` FROM organizations WHERE id=?`, orgID)
	org, err = scanOrganization(row)
	if err != nil {
		return invitationPublic{}, err
	}
	return invitationPublic{OrgDisplayName: org.DisplayName, Email: email, Username: username, Methods: []string{"password"}}, nil
}

type invitationAccept struct {
	DisplayName string
	Username    string
	Password    string
}

// acceptInvitation is atomic: it validates the token, creates the user (or,
// in a later iteration, links an existing one), applies the org role and
// project grants, marks the invitation accepted, and issues a session.
func (s *identityStore) acceptInvitation(token string, in invitationAccept) (User, string, string, Organization, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, "", "", Organization{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var id, orgID, email, invUsername, orgRole, grantsJSON, status, expiresAt string
	err = tx.QueryRow(`SELECT id, org_id, email, username, org_role, grants, status, expires_at FROM invitations WHERE token_hash=?`, hashSecret(token)).
		Scan(&id, &orgID, &email, &invUsername, &orgRole, &grantsJSON, &status, &expiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, "", "", Organization{}, errInvitationNotFound
		}
		return User{}, "", "", Organization{}, err
	}
	if status == "revoked" {
		return User{}, "", "", Organization{}, errInvitationRevoked
	}
	if status != "pending" {
		return User{}, "", "", Organization{}, errInvitationNotFound
	}
	expires, _ := parseTime(expiresAt)
	now := s.now()
	if !expires.After(now) {
		return User{}, "", "", Organization{}, errInvitationExpired
	}
	if len(in.Password) < 12 {
		return User{}, "", "", Organization{}, errPasswordTooShort
	}
	username := in.Username
	if username == "" {
		username = invUsername
	}
	if username == "" && email != "" {
		username = strings.SplitN(email, "@", 2)[0]
	}
	displayName := in.DisplayName
	if displayName == "" {
		displayName = username
	}
	username, displayName, err = validateUserInput(username, displayName)
	if err != nil {
		return User{}, "", "", Organization{}, err
	}
	userID, err := newID("usr")
	if err != nil {
		return User{}, "", "", Organization{}, err
	}
	if _, err := tx.Exec(`INSERT INTO users(id, username, display_name, email, org_role, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, username, displayName, email, orgRole, formatTime(now)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return User{}, "", "", Organization{}, fmt.Errorf("username %q already exists", username)
		}
		return User{}, "", "", Organization{}, err
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		return User{}, "", "", Organization{}, err
	}
	if _, err := tx.Exec(`INSERT INTO passwords(user_id, hash, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		userID, hash, formatTime(now), formatTime(now)); err != nil {
		return User{}, "", "", Organization{}, err
	}
	var grants []Grant
	_ = json.Unmarshal([]byte(grantsJSON), &grants)
	for _, g := range grants {
		if _, err := tx.Exec(`INSERT INTO memberships(repo_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(repo_id, user_id) DO UPDATE SET role=excluded.role, updated_at=excluded.updated_at`,
			g.ProjectID, userID, g.Role, formatTime(now), formatTime(now)); err != nil {
			return User{}, "", "", Organization{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE invitations SET status='accepted', accepted_at=? WHERE id=?`, formatTime(now), id); err != nil {
		return User{}, "", "", Organization{}, err
	}
	session, _, csrf, err := issueCredentialTx(tx, userID, "session", "browser session", now, now.Add(sessionLifetime))
	if err != nil {
		return User{}, "", "", Organization{}, err
	}
	if err := appendAuditTx(tx, userID, "invitation.accept", "invitation", id, "success", now); err != nil {
		return User{}, "", "", Organization{}, err
	}
	org, err := func() (Organization, error) {
		row := tx.QueryRow(`SELECT `+orgColumns+` FROM organizations WHERE id=?`, orgID)
		return scanOrganization(row)
	}()
	if err != nil {
		return User{}, "", "", Organization{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, "", "", Organization{}, err
	}
	user := User{ID: userID, Username: username, DisplayName: displayName, Email: email, OrgRole: orgRole, CreatedAt: now}
	return user, session, csrf, org, nil
}

// sendInvitationEmail sends the invitation link over SMTP when settings are
// configured, using only the standard library. It never blocks onboarding:
// callers treat a delivery failure as "emailed: false" and log it, since the
// wizard always shows the link as a fallback (RFC 0005: "the wizard never
// blocks on email").
func sendInvitationEmail(settings AuthMethodSettings, smtpPassword, to, orgName, link string) error {
	if !settings.SMTP.Enabled || to == "" {
		return errors.New("smtp not configured")
	}
	addr := fmt.Sprintf("%s:%d", settings.SMTP.Host, settings.SMTP.Port)
	from := settings.SMTP.From
	if from == "" {
		from = settings.SMTP.Username
	}
	subject := fmt.Sprintf("Subject: You're invited to %s on re_gent\r\n", orgName)
	body := fmt.Sprintf("You have been invited to join %s on re_gent.\r\n\r\nAccept the invitation:\r\n%s\r\n", orgName, link)
	msg := []byte(subject + "\r\n" + body)
	var auth smtp.Auth
	if settings.SMTP.Username != "" {
		auth = smtp.PlainAuth("", settings.SMTP.Username, smtpPassword, settings.SMTP.Host)
	}
	return smtp.SendMail(addr, auth, from, []string{to}, msg)
}
