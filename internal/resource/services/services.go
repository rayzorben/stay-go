// Package services implements the "services" resource.
//
// File layout (Single Responsibility per file):
//   services.go  — Resource struct, constructor, Type(), shared helpers
//   knowledge.go — GatherKnowledge (systemctl is-enabled queries)
//   plan.go      — BuildPlan (desired-vs-actual diff)
//   execute.go   — Execute (enable / restart / disable via systemctl)
package services

import (
	"context"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/executor"
)

// Resource implements engine.Resource for systemd service management.
// Supports both system services (default) and user services (user: true).
type Resource struct {
	cfg         *config.Config
	exec        *executor.Executor
	nodeConfigs map[string]*config.ServiceEntry // nodeID → config entry; populated in BuildPlan
}

// New creates a services Resource.
func New(cfg *config.Config, exec *executor.Executor) *Resource {
	return &Resource{
		cfg:         cfg,
		exec:        exec,
		nodeConfigs: make(map[string]*config.ServiceEntry),
	}
}

// Type implements engine.Resource.
func (r *Resource) Type() string { return "services" }

// ─── Shared helpers ───────────────────────────────────────────────────────────

// systemctlArgs builds an argument list for systemctl, prepending --user for
// user-scoped services so callers don't need to handle this branching.
func systemctlArgs(userService bool, args ...string) []string {
	if userService {
		return append([]string{"--user"}, args...)
	}
	return args
}

// serviceHash returns a deterministic hash of the config fields that describe
// the desired service state. The depends field is intentionally excluded — it
// describes relationships, not the service's own state.
func serviceHash(e *config.ServiceEntry) string {
	return config.Hash(struct {
		Service string
		User    bool
		Enabled bool
	}{e.Service, e.User, e.IsEnabled()})
}

// isEnabled checks whether a service is currently enabled via systemctl.
// Exit code 0 → enabled; any other code → not enabled or not found.
func isEnabled(ctx context.Context, exec *executor.Executor, service string, userService bool) bool {
	result, _ := exec.Run(ctx, executor.Options{},
		"systemctl", systemctlArgs(userService, "is-enabled", "--quiet", service)...)
	return result != nil && result.ExitCode == 0
}
