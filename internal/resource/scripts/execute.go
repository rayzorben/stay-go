package scripts

import (
	"context"
	"fmt"

	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/executor"
	"github.com/rayben/stay-go/internal/state"
)

// Execute runs the script or removes it from tracking.
// State is updated on success.
// Implements engine.NodeExecutor.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	switch node.Action {
	case engine.ActionAdd, engine.ActionUpdate:
		entry, ok := r.nodeConfigs[node.ID]
		if !ok {
			return fmt.Errorf("no config found for script %q", node.DisplayName)
		}
		result, err := r.exec.Run(ctx, executor.Options{Sudo: entry.Sudo}, "bash", entry.Script)
		if err != nil {
			return fmt.Errorf("running script %q: %w\nstderr: %s", entry.Script, err, result.Stderr)
		}
		st.Set(node.ID, node.ConfigHash, node.Level, node.StateData)

	case engine.ActionRemove:
		// Scripts have no system-level undo — just stop tracking.
		st.Delete(node.ID)
	}

	return nil
}
