package groups

import (
	"context"
	"fmt"

	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// Execute creates or deletes a system group.
// State is updated on success.
// Implements engine.NodeExecutor.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	name := node.DisplayName

	switch node.Action {
	case engine.ActionAdd:
		// -f: succeed if the group already exists — a package installed
		// earlier in the same run (docker → docker group) may have created it
		// after knowledge was gathered.
		result, err := r.exec.Run(ctx, executor.Options{Sudo: true}, "groupadd", "-f", name)
		if err != nil {
			return fmt.Errorf("creating group %q: %w\nstderr: %s", name, err, result.Stderr)
		}
		st.Set(node.ID, node.ConfigHash, node.SourceFile, node.StateData)

	case engine.ActionRemove:
		result, err := r.exec.Run(ctx, executor.Options{Sudo: true}, "groupdel", name)
		if err != nil {
			return fmt.Errorf("deleting group %q: %w\nstderr: %s", name, err, result.Stderr)
		}
		st.Delete(node.ID)
	}

	return nil
}
