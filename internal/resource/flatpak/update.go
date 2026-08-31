package flatpak

import (
	"context"
	"fmt"

	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// runtimesNodeID is the ephemeral plan node that updates all installed
// flatpak runtimes (--update mode). Never persisted to state.
const runtimesNodeID = "flatpak/runtimes"

// BuildUpdatePlan emits one ActionUpgrade node per tracked app, plus a single
// runtimes node — per-app updates pull the runtimes they need, but shared
// runtimes with no dependent app change would otherwise go stale.
// Implements engine.Updater.
func (r *Resource) BuildUpdatePlan(_ context.Context, st *state.State) ([]*engine.PlanNode, error) {
	var nodes []*engine.PlanNode
	for _, app := range r.cfg.Flatpak.Apps {
		id := appNodeID(app.AppID)
		source := app.SourceFile
		if source == "" {
			source = "common"
		}
		node := &engine.PlanNode{
			ID:           id,
			ResourceType: "flatpak",
			DisplayName:  app.AppID,
			Action:       engine.ActionUpgrade,
			Locked:       app.Lock,
			SourceFile:   source,
			Description:  "flatpak update --user",
		}
		if !st.Has(id) {
			node.Action = engine.ActionSkip
			node.SkipReason = "not applied yet — run stay-go first"
		}
		nodes = append(nodes, node)
	}
	if len(nodes) > 0 {
		nodes = append(nodes, &engine.PlanNode{
			ID:           runtimesNodeID,
			ResourceType: "flatpak",
			DisplayName:  "runtimes",
			Action:       engine.ActionUpgrade,
			SourceFile:   "common",
			Description:  "flatpak update --user --runtime",
		})
	}
	return nodes, nil
}

// executeUpgrade updates a single app (or all runtimes for the runtimes node).
// State is never touched.
func (r *Resource) executeUpgrade(ctx context.Context, node *engine.PlanNode) error {
	args := []string{"update", "--noninteractive", "--user"}
	if node.ID == runtimesNodeID {
		args = append(args, "--runtime")
	} else {
		args = append(args, node.DisplayName)
	}
	result, err := r.exec.Run(ctx, executor.Options{}, "flatpak", args...)
	if err != nil {
		return fmt.Errorf("updating flatpak %q: %w\nstderr: %s", node.DisplayName, err, result.Stderr)
	}
	return nil
}
