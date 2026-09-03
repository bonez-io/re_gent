package remote

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/bonez-io/re_gent/internal/remotetest"
)

// The headline guarantee: enrolling the same fingerprint twice returns the
// same project the second time instead of creating a duplicate.
func TestEnrollProjectIsIdempotentByFingerprint(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()
	ctx := context.Background()

	req := EnrollProjectRequest{
		Fingerprint: "fp-abc123",
		Remote:      "github.com/acme/api",
		RootCommit:  "deadbeef",
		DisplayName: "api",
	}

	first, err := EnrollProject(ctx, http.DefaultClient, srv.URL(), "", req)
	if err != nil {
		t.Fatalf("first EnrollProject: %v", err)
	}
	if !first.Created {
		t.Errorf("first enrollment should be Created=true, got %+v", first)
	}
	if first.Project.ID == "" {
		t.Fatal("first enrollment returned an empty project id")
	}

	second, err := EnrollProject(ctx, http.DefaultClient, srv.URL(), "", req)
	if err != nil {
		t.Fatalf("second EnrollProject: %v", err)
	}
	if second.Created {
		t.Errorf("second enrollment of the same fingerprint should not be Created")
	}
	if second.Project.ID != first.Project.ID {
		t.Errorf("second enrollment returned a different project: %q vs %q", second.Project.ID, first.Project.ID)
	}
	if srv.ProjectCount() != 1 {
		t.Errorf("server holds %d projects, want 1", srv.ProjectCount())
	}
}

func TestEnrollProjectRequiresDisplayNameWithoutFingerprint(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()

	_, err := EnrollProject(context.Background(), http.DefaultClient, srv.URL(), "", EnrollProjectRequest{})
	if err == nil {
		t.Fatal("expected an error enrolling with neither a fingerprint nor a display name")
	}
	var se *ServerError
	if !errors.As(err, &se) || se.Code != "invalid_request" {
		t.Fatalf("error = %v, want invalid_request", err)
	}
}

func TestEnrollProjectOrgRoute(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()

	result, err := EnrollProject(context.Background(), http.DefaultClient, srv.URL(), "", EnrollProjectRequest{
		Org: "acme", DisplayName: "internal-tool",
	})
	if err != nil {
		t.Fatalf("EnrollProject with org: %v", err)
	}
	if result.Project.OrgID != "acme" {
		t.Errorf("OrgID = %q, want acme", result.Project.OrgID)
	}
}

func TestEnrollProjectFingerprintConflict(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()
	srv.ForceFingerprintConflict("fp-taken")

	_, err := EnrollProject(context.Background(), http.DefaultClient, srv.URL(), "", EnrollProjectRequest{
		Fingerprint: "fp-taken", Remote: "github.com/acme/api", DisplayName: "api",
	})
	if !IsFingerprintConflict(err) {
		t.Fatalf("IsFingerprintConflict(%v) = false, want true", err)
	}
	if srv.ProjectCount() != 0 {
		t.Errorf("a conflicted enrollment must not create a project, got %d", srv.ProjectCount())
	}
}

// A fork's remote differs but its root commit matches a public upstream
// project: the server reports the match so the CLI can decide what to do.
func TestEnrollProjectDetectsForkByRootCommit(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()
	ctx := context.Background()

	upstream, err := EnrollProject(ctx, http.DefaultClient, srv.URL(), "", EnrollProjectRequest{
		Org: "upstream-org", Fingerprint: "fp-upstream", Remote: "github.com/upstream/project", RootCommit: "root-1", DisplayName: "project",
	})
	if err != nil {
		t.Fatalf("enroll upstream: %v", err)
	}
	srv.MarkProjectPublic(upstream.Project.ID)

	fork, err := EnrollProject(ctx, http.DefaultClient, srv.URL(), "", EnrollProjectRequest{
		Org: "fork-org", Fingerprint: "fp-fork", Remote: "github.com/contributor/project", RootCommit: "root-1", DisplayName: "project",
	})
	if err != nil {
		t.Fatalf("enroll fork: %v", err)
	}
	if fork.Upstream == nil {
		t.Fatal("expected the fork enrollment to report an upstream match")
	}
	if fork.Upstream.ID != upstream.Project.ID {
		t.Errorf("Upstream.ID = %q, want %q", fork.Upstream.ID, upstream.Project.ID)
	}
	// The fork still gets its own project in its own organization: an
	// upstream hint is not the same as being denied enrollment.
	if fork.Project.ID == upstream.Project.ID {
		t.Errorf("fork was enrolled as the same project as upstream")
	}
}

func TestGetProjectConfirmsExistence(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()
	ctx := context.Background()

	created, err := EnrollProject(ctx, http.DefaultClient, srv.URL(), "", EnrollProjectRequest{DisplayName: "solo"})
	if err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	got, err := GetProject(ctx, http.DefaultClient, srv.URL(), "", created.Project.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.ID != created.Project.ID || got.DisplayName != "solo" {
		t.Errorf("GetProject = %+v, want id %q display_name solo", got, created.Project.ID)
	}

	if _, err := GetProject(ctx, http.DefaultClient, srv.URL(), "", "prj_does_not_exist"); err == nil {
		t.Fatal("expected an error fetching a project that does not exist")
	}
}
