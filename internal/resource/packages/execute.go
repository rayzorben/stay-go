package packages

import (
	"context"
	"fmt"

	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/executor"
	"github.com/rayben/stay-go/internal/state"
)

// Execute installs, updates, or removes a single package.
// State is updated on success.
// Implements engine.NodeExecutor.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	if err := r.ensureManager(ctx); err != nil {
		return err
	}
	name := node.DisplayName
	opts := executor.Options{Sudo: r.manager.NeedsSudo, Env: r.manager.Env}

	switch node.Action {
	case engine.ActionAdd, engine.ActionUpdate:
		cmd := r.manager.InstallCmd
		result, err := r.exec.Run(ctx, opts, cmd[0], append(cmd[1:], name)...)
		if err != nil {
			return fmt.Errorf("installing package %q: %w\nstderr: %s", name, err, result.Stderr)
		}
		// If a higher-priority manager (e.g. paru) was just installed, reset the
		// cached manager so subsequent packages use it instead of the current one.
		if betterManagerNowAvailable(r.manager) {
			r.manager = nil
		}
		st.Set(node.ID, node.ConfigHash, node.Level, node.StateData)

	case engine.ActionRemove:
		cmd := r.manager.RemoveCmd
		result, err := r.exec.Run(ctx, opts, cmd[0], append(cmd[1:], name)...)
		if err != nil {
			return fmt.Errorf("removing package %q: %w\nstderr: %s", name, err, result.Stderr)
		}
		st.Delete(node.ID)
	}

	return nil
}
