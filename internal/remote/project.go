package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxProjectResponseBytes bounds a project API response body. A project
// object plus an optional upstream summary is a few hundred bytes; this is
// generous headroom, not a size the server is expected to approach.
const maxProjectResponseBytes = 64 << 10

// ProjectSource is the fingerprint material a project was enrolled with, when
// known. A project created only from --as (no fingerprint, RFC 0004 §
// "Project enrollment") has no source at all.
type ProjectSource struct {
	Remote      string `json:"remote,omitempty"`
	RootCommit  string `json:"root_commit,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Project is the server's representation of an enrolled project (RFC 0004).
type Project struct {
	ID          string         `json:"id"`
	DisplayName string         `json:"display_name"`
	OrgID       string         `json:"org_id"`
	Visibility  string         `json:"visibility"`
	CreatedAt   string         `json:"created_at"`
	Source      *ProjectSource `json:"source,omitempty"`
}

// ProjectUpstream names the public project a fork's root commit matched.
type ProjectUpstream struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// EnrollProjectRequest is what the client already knows before asking the
// server to create or attach to a project.
type EnrollProjectRequest struct {
	// Org selects POST /api/v1/orgs/{org}/projects when non-empty, else
	// POST /api/v1/projects.
	Org string
	// Fingerprint, Remote, and RootCommit come from sourceFingerprint. All
	// three are omitted from the request when Fingerprint is empty (a
	// directory with no fingerprint, RFC 0004): the server then always
	// creates a new project.
	Fingerprint string
	Remote      string
	RootCommit  string
	// DisplayName is required when Fingerprint is empty, and used as a hint
	// otherwise (the server may already have a display name for an existing
	// project at this fingerprint).
	DisplayName string
}

// EnrollProjectResult is the server's answer to enrollment.
type EnrollProjectResult struct {
	Project Project
	// Created is true for a 201 (a new project was made) and false for a 200
	// (an existing project, matched by fingerprint, was returned instead —
	// the connect-once guarantee).
	Created bool
	// Upstream is set when Project's root commit matched a public project's:
	// this repository looks like a fork. Nil means no match was found.
	Upstream *ProjectUpstream
}

// ServerError is a structured {"error","code"} response from the project
// API, preserving the HTTP status for callers that branch on it.
type ServerError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *ServerError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("server returned %d (%s)", e.StatusCode, e.Code)
}

// IsFingerprintConflict reports whether err is the 409 the server returns
// when this fingerprint is already enrolled in the target organization but
// the caller lacks access to it.
func IsFingerprintConflict(err error) bool {
	var se *ServerError
	return asServerError(err, &se) && se.Code == "fingerprint_conflict"
}

// IsNotSignedIn reports whether err is the 401 the project API returns for an
// unauthenticated request.
func IsNotSignedIn(err error) bool {
	var se *ServerError
	return asServerError(err, &se) && se.Code == "unauthenticated"
}

// IsForbidden reports whether err is the 403 the project API returns when the
// caller may not enroll a project in the named organization.
func IsForbidden(err error) bool {
	var se *ServerError
	return asServerError(err, &se) && se.Code == "forbidden"
}

func asServerError(err error, target **ServerError) bool {
	se, ok := err.(*ServerError)
	if !ok {
		return false
	}
	*target = se
	return true
}

// EnrollProject calls the project enrollment endpoint (RFC 0004, "Project
// enrollment: connect once"): POST /api/v1/projects, or
// POST /api/v1/orgs/{org}/projects when req.Org is set.
//
// This is the client half of the connect-once guarantee: a fingerprint the
// server has already seen in this organization comes back as the existing
// project (200, Created=false) instead of a duplicate. It is a single
// attempt with no retry loop — unlike the object/ref protocol, a blind retry
// here is not free of side effects when the request carries no fingerprint
// (a non-git directory identified only by --as), where the server has no
// unique key to dedupe against and a retried POST could create two projects.
// A caller that wants resilience against a transient failure retries the
// whole `rgt connect` invocation, which a person is already watching.
func EnrollProject(ctx context.Context, client *http.Client, serverURL, token string, req EnrollProjectRequest) (EnrollProjectResult, error) {
	if client == nil {
		client = http.DefaultClient
	}
	payload := map[string]string{}
	if req.Fingerprint != "" {
		payload["fingerprint"] = req.Fingerprint
	}
	if req.Remote != "" {
		payload["remote"] = req.Remote
	}
	if req.RootCommit != "" {
		payload["root_commit"] = req.RootCommit
	}
	if req.DisplayName != "" {
		payload["display_name"] = req.DisplayName
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return EnrollProjectResult{}, fmt.Errorf("encode enrollment request: %w", err)
	}

	endpoint := strings.TrimRight(serverURL, "/") + "/api/v1/projects"
	if req.Org != "" {
		endpoint = strings.TrimRight(serverURL, "/") + "/api/v1/orgs/" + strings.TrimSpace(req.Org) + "/projects"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return EnrollProjectResult{}, fmt.Errorf("build enrollment request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return EnrollProjectResult{}, fmt.Errorf("POST %s: %w", redactURL(endpoint), err)
	}
	defer closeBody(resp)

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProjectResponseBytes+1))
	if err != nil {
		return EnrollProjectResult{}, fmt.Errorf("read enrollment response: %w", err)
	}
	if len(data) > maxProjectResponseBytes {
		return EnrollProjectResult{}, fmt.Errorf("enrollment response exceeds size limit")
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var body struct {
			Project  Project          `json:"project"`
			Created  bool             `json:"created"`
			Upstream *ProjectUpstream `json:"upstream"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			return EnrollProjectResult{}, fmt.Errorf("decode enrollment response: %w", err)
		}
		if body.Project.ID == "" {
			return EnrollProjectResult{}, fmt.Errorf("server returned an empty project id")
		}
		return EnrollProjectResult{Project: body.Project, Created: body.Created || resp.StatusCode == http.StatusCreated, Upstream: body.Upstream}, nil
	default:
		return EnrollProjectResult{}, decodeServerError(resp.StatusCode, data)
	}
}

// GetProject calls GET /api/v1/projects/{id}, confirming a project exists and
// the caller can still see it. This is what `rgt connect` uses to turn an
// already-project-id-bound repository into a no-op instead of trusting the
// binding file on faith — the file is a claim, not proof.
func GetProject(ctx context.Context, client *http.Client, serverURL, token, id string) (Project, error) {
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := strings.TrimRight(serverURL, "/") + "/api/v1/projects/" + id
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Project{}, fmt.Errorf("build project request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return Project{}, fmt.Errorf("GET %s: %w", redactURL(endpoint), err)
	}
	defer closeBody(resp)

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProjectResponseBytes+1))
	if err != nil {
		return Project{}, fmt.Errorf("read project response: %w", err)
	}
	if len(data) > maxProjectResponseBytes {
		return Project{}, fmt.Errorf("project response exceeds size limit")
	}

	if resp.StatusCode != http.StatusOK {
		return Project{}, decodeServerError(resp.StatusCode, data)
	}
	var body struct {
		Project Project `json:"project"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		// Some servers may answer with the project object directly rather
		// than wrapped; accept either shape rather than failing a working
		// server over a formatting choice this RFC did not pin down.
		var direct Project
		if err2 := json.Unmarshal(data, &direct); err2 == nil && direct.ID != "" {
			return direct, nil
		}
		return Project{}, fmt.Errorf("decode project response: %w", err)
	}
	if body.Project.ID == "" {
		return Project{}, fmt.Errorf("server returned an empty project id")
	}
	return body.Project, nil
}

func decodeServerError(status int, data []byte) error {
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	_ = json.Unmarshal(data, &body)
	msg := body.Error
	if msg == "" {
		msg = fmt.Sprintf("server returned %d", status)
	}
	return &ServerError{StatusCode: status, Code: body.Code, Message: msg}
}
