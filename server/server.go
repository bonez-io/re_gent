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

func New(dataDir string, opts ...Option) (*Server, error) {
	return internalserver.New(dataDir, opts...)
}

func ValidateRepoID(id string) error { return internalserver.ValidateRepoID(id) }

func WithAccessController(controller serverauth.Controller) Option {
	return internalserver.WithAccessController(controller)
}

func WithSkillsDir(dir string) Option { return internalserver.WithSkillsDir(dir) }

func WithMaxObjectBytes(n int64) Option { return internalserver.WithMaxObjectBytes(n) }

func WithLogger(logger *log.Logger) Option { return internalserver.WithLogger(logger) }

func WithBinariesDir(dir string) Option { return internalserver.WithBinariesDir(dir) }
