package distrobox

import (
	"context"
	"fmt"

	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// BuildUpdatePlan emits one ActionUpgrade node per tracked box, running
// `distrobox upgrade <name>` — the container's own package manager upgrade,
// executed inside the box. Only host-level box nodes are upgraded; in-box
// apply nodes are config application, not version refresh.
// Implements engine.Updater.
func (r *Resource) BuildUpdatePlan(_ context.Context, st *state.State) ([]*engine.PlanNode, error) {
	var nodes []*engine.PlanNode
	for i := range r.cfg.Distrobox {
		entry := &r.cfg.Distrobox[i]
		id := boxNodeID(entry.Name)
		r.nodeConfigs[id] = entry

		source := entry.SourceFile
		if source == "" {
			source = "common"
		}
		node := &engine.PlanNode{
			ID:           id,
			ResourceType: "distrobox",
			DisplayName:  entry.Name,
			Action:       engine.ActionUpgrade,
			Locked:       entry.Lock,
			SourceFile:   source,
			Description:  "distrobox upgrade (in-box package manager)",
		}
		if !st.Has(id) {
			node.Action = engine.ActionSkip
			node.SkipReason = "not applied yet — run stay-go first"
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// executeUpgrade runs `distrobox upgrade <name>`. Output is streamed — the
// in-box package manager can run for minutes. State is never touched.
func (r *Resource) executeUpgrade(ctx context.Context, name string) error {
	result, err := r.exec.Run(ctx, executor.Options{Stream: true}, "distrobox", "upgrade", name)
	if err != nil {
		return fmt.Errorf("upgrading distrobox %q: %w\nstderr: %s", name, err, result.Stderr)
	}
	return nil
}
