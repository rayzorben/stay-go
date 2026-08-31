package compose

import (
	"context"
	"fmt"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// BuildUpdatePlan emits one ActionUpgrade node per tracked compose project:
// pull the project's images and `up -d` so compose recreates only the services
// whose image changed. This is the supported way to refresh a stay-go-managed
// project — the rendered files live in a private cache dir, so running
// `docker compose pull` by hand is impractical.
// Implements engine.Updater.
func (r *Resource) BuildUpdatePlan(_ context.Context, st *state.State) ([]*engine.PlanNode, error) {
	var nodes []*engine.PlanNode
	for i := range r.cfg.Compose {
		entry := &r.cfg.Compose[i]
		project := entry.ProjectName()
		id := config.NodeID("compose", project)
		r.nodeConfigs[id] = entry

		source := entry.SourceFile
		if source == "" {
			source = "common"
		}
		rt := resolveRuntime(entry.Runtime)

		node := &engine.PlanNode{
			ID:           id,
			ResourceType: "compose",
			DisplayName:  project,
			Action:       engine.ActionUpgrade,
			Locked:       entry.Lock,
			NeedsSudo:    entry.Sudo,
			SourceFile:   source,
			Description:  rt + " compose pull && up -d",
		}
		switch {
		case !st.Has(id):
			node.Action = engine.ActionSkip
			node.SkipReason = "not applied yet — run stay-go first"
		case !isDir(entry.Path):
			node.Action = engine.ActionSkip
			node.SkipReason = "compose path not found: " + entry.Path
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// executeUpgrade renders the project (substituting secrets), pulls its images,
// and runs `up -d --remove-orphans` — compose recreates only the services whose
// image changed. State is never touched.
func (r *Resource) executeUpgrade(ctx context.Context, entry *config.ComposeEntry) error {
	rt := resolveRuntime(entry.Runtime)
	dir, err := renderProject(entry, r.cfg)
	if err != nil {
		return err
	}
	opts := executor.Options{Sudo: entry.Sudo}

	pullArgs := append(composeArgs(dir, entry), "pull")
	result, err := r.exec.Run(ctx, opts, rt, pullArgs...)
	if err != nil {
		return fmt.Errorf("%s compose pull %q: %w\nstderr: %s", rt, entry.ProjectName(), err, result.Stderr)
	}

	upArgs := append(composeArgs(dir, entry), "up", "-d", "--remove-orphans")
	result, err = r.exec.Run(ctx, opts, rt, upArgs...)
	if err != nil {
		return fmt.Errorf("%s compose up %q: %w\nstderr: %s", rt, entry.ProjectName(), err, result.Stderr)
	}
	return nil
}
