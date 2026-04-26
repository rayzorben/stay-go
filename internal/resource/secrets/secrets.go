// Package secrets implements the "secrets" resource.
//
// File layout:
//
//	secrets.go  — Resource struct, constructor, Type()
//	knowledge.go — GatherKnowledge
//	plan.go      — BuildPlan
//	execute.go   — Execute
package secrets

import (
	"github.com/rayzorben/stay-go/internal/config"
	secretspkg "github.com/rayzorben/stay-go/internal/secrets"
)

// Resource implements engine.Resource for encrypted secrets.
type Resource struct {
	cfg     *config.Config
	mgr     *secretspkg.Manager
	entries map[string]secretspkg.Entry // nodeID → entry; populated in BuildPlan
}

// New creates a secrets Resource. The Manager is unlocked lazily on first Execute.
func New(cfg *config.Config) *Resource {
	return &Resource{
		cfg:     cfg,
		mgr:     secretspkg.New(),
		entries: make(map[string]secretspkg.Entry),
	}
}

// Type implements engine.Resource.
func (r *Resource) Type() string { return "secrets" }
