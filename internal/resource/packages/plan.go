package packages

import (
	"context"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/state"
)

// BuildPlan computes PlanNodes for all managed packages.
// Uses engine.DetermineAction for action logic and engine.StateRemovals for
// detecting packages to remove — both shared helpers (DRY).
// Implements engine.Planner.
func (r *Resource) BuildPlan(_ context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	configSet := make(map[string]bool, len(r.cfg.Packages))
	for _, p := range r.cfg.Packages {
		configSet[p.Name] = true
	}

	var nodes []*engine.PlanNode

	for _, p := range r.cfg.Packages {
		id := config.NodeID("packages", p.Name)
		hash := config.Hash(p.Name)
		action := engine.DetermineAction(id, knowledge[id], hash, st)

		level := p.Level
		if level == "" {
			level = "common"
		}
		action, levelDesc := engine.CheckLevelChange(id, level, action, st)

		desc := describePackageChange(action, r.manager)
		if levelDesc != "" {
			desc = levelDesc
		}

		nodes = append(nodes, &engine.PlanNode{
			ID:           id,
			ResourceType: "packages",
			DisplayName:  p.Name,
			Action:       action,
			ConfigHash:   hash,
			NeedsSudo:    r.manager != nil && r.manager.NeedsSudo,
			Level:        level,
			Description:  desc,
			StateData:    map[string]interface{}{"name": p.Name},
		})
	}

	// Append REMOVE nodes for packages tracked in state but removed from config.
	removals := engine.StateRemovals("packages", configSet, st)
	for _, n := range removals {
		n.Description = describePackageChange(engine.ActionRemove, r.manager)
	}
	nodes = append(nodes, removals...)
	return nodes, nil
}

// describePackageChange returns a brief description of the package operation.
// The manager may be nil if detection hasn't run yet (plan is called before execute).
func describePackageChange(action engine.ActionType, mgr *PackageManager) string {
	via := ""
	if mgr != nil {
		via = " via " + mgr.Name
	}
	switch action {
	case engine.ActionAdd:
		return "install" + via
	case engine.ActionRemove:
		return "remove" + via
	case engine.ActionUpdate:
		return "reinstall" + via
	}
	return ""
}
