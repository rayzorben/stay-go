// Package commands implements the "commands" resource.
//
// File layout (Single Responsibility per file):
//
//	commands.go  — Resource struct, constructor, Type()
//	knowledge.go — GatherKnowledge
//	plan.go      — BuildPlan
//	execute.go   — Execute
package commands

import (
	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/executor"
)

// Resource implements engine.Resource for named inline commands.
type Resource struct {
	cfg         *config.Config
	exec        *executor.Executor
	nodeConfigs map[string]*config.CommandEntry // nodeID → config entry; populated in BuildPlan
}

// New creates a commands Resource.
func New(cfg *config.Config, exec *executor.Executor) *Resource {
	return &Resource{
		cfg:         cfg,
		exec:        exec,
		nodeConfigs: make(map[string]*config.CommandEntry),
	}
}

// Type implements engine.Resource.
func (r *Resource) Type() string { return "commands" }
