package services

import (
	"context"
	"fmt"

	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// Execute enables, restarts, or disables a single systemd service.
// State is updated on success.
// Implements engine.NodeExecutor.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	entry, ok := r.nodeConfigs[node.ID]
	if !ok {
		// REMOVE nodes for services deleted from config have no config entry;
		// reconstruct the service name and scope from persisted state instead.
		if node.Action == engine.ActionRemove {
			name, isUser := serviceFromState(node, st)
			return r.removeService(ctx, node, name, isUser, st)
		}
		return fmt.Errorf("no config found for service %q", node.DisplayName)
	}

	isUser := entry.User
	// System services require sudo; user services run as the invoking user.
	opts := executor.Options{Sudo: !isUser}
	name := entry.Service // DisplayName includes "user/" or "system/" prefix — use raw name

	switch node.Action {
	case engine.ActionAdd:
		args := []string{"enable"}
		if entry.IsNow() {
			args = append(args, "--now")
		}
		args = append(args, name)
		result, err := r.exec.Run(ctx, opts, "systemctl", systemctlArgs(isUser, args...)...)
		if err != nil {
			return fmt.Errorf("enabling service %q: %w\nstderr: %s", name, err, result.Stderr)
		}
		st.Set(node.ID, node.ConfigHash, node.SourceFile, node.StateData)

	case engine.ActionUpdate:
		// Reload unit files then restart to apply any changes.
		r.exec.Run(ctx, opts, "systemctl", systemctlArgs(isUser, "daemon-reload")...) //nolint:errcheck
		result, err := r.exec.Run(ctx, opts,
			"systemctl", systemctlArgs(isUser, "restart", name)...)
		if err != nil {
			return fmt.Errorf("restarting service %q: %w\nstderr: %s", name, err, result.Stderr)
		}
		st.Set(node.ID, node.ConfigHash, node.SourceFile, node.StateData)

	case engine.ActionRemove:
		return r.removeService(ctx, node, name, isUser, st)
	}

	return nil
}

// serviceFromState reconstructs a removed service's name and scope from
// persisted state data. Falls back to the node display name (StateRemovals
// sets it to the raw ID suffix, i.e. the service name) and system scope.
func serviceFromState(node *engine.PlanNode, st *state.State) (name string, isUser bool) {
	name = node.DisplayName
	if entry, ok := st.Get(node.ID); ok && entry.Data != nil {
		if s, _ := entry.Data["service"].(string); s != "" {
			name = s
		}
		isUser, _ = entry.Data["user"].(bool)
	}
	return name, isUser
}

// removeService disables a service and clears its state entry. If the service
// is already not enabled (manually disabled, unit file gone, or no running
// systemd), there is nothing to undo — clean up state instead of failing
// forever on a disable that can never succeed.
func (r *Resource) removeService(ctx context.Context, node *engine.PlanNode, name string, isUser bool, st *state.State) error {
	if !isEnabled(ctx, r.exec, name, isUser) {
		st.Delete(node.ID)
		return nil
	}
	opts := executor.Options{Sudo: !isUser}
	result, err := r.exec.Run(ctx, opts,
		"systemctl", systemctlArgs(isUser, "disable", "--now", name)...)
	if err != nil {
		return fmt.Errorf("disabling service %q: %w\nstderr: %s", name, err, result.Stderr)
	}
	st.Delete(node.ID)
	return nil
}
