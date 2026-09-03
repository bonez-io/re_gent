// Package server is the supported embedding surface for the re_gent HTTP
// server. Managed and self-hosted compositions should import this package, not
// the internal implementation.
package server

import (
	"log"

	internalserver "github.com/bonez-io/re_gent/internal/server"
	"github.com/bonez-io/re_gent/serverauth"
)

const DefaultMaxObjectBytes = internalserver.DefaultMaxObjectBytes

type Server = internalserver.Server
type Option = internalserver.Option

// StorageLocator resolves where one project's on-disk store lives. See
// internalserver.StorageLocator for the full contract and the default
// implementation's behavior.
type StorageLocator = internalserver.StorageLocator

// IngestFilter inspects one object's bytes before the core writes it. See
// internalserver.IngestFilter.
type IngestFilter = internalserver.IngestFilter

// IngestAction is the decision an IngestFilter makes about one object upload.
type IngestAction = internalserver.IngestAction

const (
	IngestAccept = internalserver.IngestAccept
	IngestReject = internalserver.IngestReject
)

// CapabilitiesFunc builds the public GET /api/v1/capabilities document for one
// request. See internalserver.CapabilitiesFunc.
type CapabilitiesFunc = internalserver.CapabilitiesFunc

// ProjectRegistry is the project-identity seam described in RFC 0004. See
// internalserver.ProjectRegistry for the full contract and the default
// filesystem+SQLite implementation's behavior.
type ProjectRegistry = internalserver.ProjectRegistry

// Project, ProjectSource, and ProjectCreate are the ProjectRegistry data
// shapes. See internalserver for field documentation.
type Project = internalserver.Project
type ProjectSource = internalserver.ProjectSource
type ProjectCreate = internalserver.ProjectCreate

// ErrProjectNotFound is returned by ProjectRegistry.Get and .Rename.
var ErrProjectNotFound = internalserver.ErrProjectNotFound

func New(dataDir string, opts ...Option) (*Server, error) {
	return internalserver.New(dataDir, opts...)
}

func ValidateRepoID(id string) error { return internalserver.ValidateRepoID(id) }

// ValidateFingerprint reports whether raw is an acceptable source fingerprint
// for the versioned project API's POST body.
func ValidateFingerprint(raw string) error { return internalserver.ValidateFingerprint(raw) }

func WithAccessController(controller serverauth.Controller) Option {
	return internalserver.WithAccessController(controller)
}

func WithSkillsDir(dir string) Option { return internalserver.WithSkillsDir(dir) }

func WithMaxObjectBytes(n int64) Option { return internalserver.WithMaxObjectBytes(n) }

func WithLogger(logger *log.Logger) Option { return internalserver.WithLogger(logger) }

func WithBinariesDir(dir string) Option { return internalserver.WithBinariesDir(dir) }

// WithStorageLocator overrides where a project's on-disk store lives. The
// default reproduces today's dataDir/repos/<id> layout.
func WithStorageLocator(locator StorageLocator) Option {
	return internalserver.WithStorageLocator(locator)
}

// WithAuditor installs the sink for mutation and denial audit events. The
// default records nothing, matching today's open-mode behavior.
func WithAuditor(auditor serverauth.Auditor) Option {
	return internalserver.WithAuditor(auditor)
}

// WithLimiter installs the quota/rate approval consulted before an object
// write, a ref move, and a project creation. The default allows everything.
func WithLimiter(limiter serverauth.Limiter) Option {
	return internalserver.WithLimiter(limiter)
}

// WithIngestFilter installs the gate consulted before an object is written.
// The default accepts every object unchanged.
func WithIngestFilter(filter IngestFilter) Option {
	return internalserver.WithIngestFilter(filter)
}

// WithCapabilities installs the builder for the public GET
// /api/v1/capabilities document. The default returns the RFC 0004 open-mode
// document: deployment "open", no auth methods, and the base feature set.
func WithCapabilities(fn CapabilitiesFunc) Option {
	return internalserver.WithCapabilities(fn)
}

// WithProjectRegistry overrides the project registry backing the versioned
// project API. The default is a filesystem+SQLite registry rooted at dataDir.
func WithProjectRegistry(registry ProjectRegistry) Option {
	return internalserver.WithProjectRegistry(registry)
}
