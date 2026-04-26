// Package scripts implements the "scripts" resource.
//
// File layout (Single Responsibility per file):
//
//	scripts.go   — Resource struct, constructor, Type(), hash helper
//	knowledge.go — GatherKnowledge (returns all configured scripts as present)
//	plan.go      — BuildPlan (hash includes file content; folder condition checks)
//	execute.go   — Execute (runs script with bash, optionally via sudo)
package scripts

import (
	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/executor"
)

// Resource implements engine.Resource for shell script management.
type Resource struct {
	cfg         *config.Config
	exec        *executor.Executor
	nodeConfigs map[string]*config.ScriptEntry // nodeID → config entry; populated in BuildPlan
}

// New creates a scripts Resource.
func New(cfg *config.Config, exec *executor.Executor) *Resource {
	return &Resource{
		cfg:         cfg,
		exec:        exec,
		nodeConfigs: make(map[string]*config.ScriptEntry),
	}
}

// Type implements engine.Resource.
func (r *Resource) Type() string { return "scripts" }

// nodeID returns the canonical node identifier for a script entry.
// Uses the resolved script path as the unique key.
func nodeID(script string) string {
	return "scripts/" + script
}
