package services

import (
	"context"
	"fmt"

	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/executor"
	"github.com/rayben/stay-go/internal/state"
)

// Execute enables, restarts, or disables a single systemd service.
// State is updated on success.
// Implements engine.NodeExecutor.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	entry, ok := r.nodeConfigs[node.ID]
	if !ok {
		return fmt.Errorf("no config found for service %q", node.DisplayName)
	}

	isUser := entry.User
	// System services require sudo; user services run as the invoking user.
	opts := executor.Options{Sudo: !isUser}
	name := entry.Service // DisplayName includes "user/" or "system/" prefix — use raw name

	switch node.Action {
	case engine.ActionAdd:
		result, err := r.exec.Run(ctx, opts,
			"systemctl", systemctlArgs(isUser, "enable", "--now", name)...)
		if err != nil {
			return fmt.Errorf("enabling service %q: %w\nstderr: %s", name, err, result.Stderr)
		}
		st.Set(node.ID, node.ConfigHash, node.Level, node.StateData)

	case engine.ActionUpdate:
		// Reload unit files then restart to apply any changes.
		r.exec.Run(ctx, opts, "systemctl", systemctlArgs(isUser, "daemon-reload")...) //nolint:errcheck
		result, err := r.exec.Run(ctx, opts,
			"systemctl", systemctlArgs(isUser, "restart", name)...)
		if err != nil {
			return fmt.Errorf("restarting service %q: %w\nstderr: %s", name, err, result.Stderr)
		}
		st.Set(node.ID, node.ConfigHash, node.Level, node.StateData)

	case engine.ActionRemove:
		result, err := r.exec.Run(ctx, opts,
			"systemctl", systemctlArgs(isUser, "disable", "--now", name)...)
		if err != nil {
			return fmt.Errorf("disabling service %q: %w\nstderr: %s", name, err, result.Stderr)
		}
		st.Delete(node.ID)
	}

	return nil
}
