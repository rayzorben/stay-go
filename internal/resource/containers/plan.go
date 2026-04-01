package containers

import (
	"context"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/state"
)

// BuildPlan computes PlanNodes for managed containers.
// Uses engine.DetermineAction for standard action logic (DRY).
// Implements engine.Planner.
func (r *Resource) BuildPlan(_ context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	configSet := make(map[string]bool, len(r.cfg.Containers))
	var nodes []*engine.PlanNode

	for i := range r.cfg.Containers {
		entry := &r.cfg.Containers[i]
		name := entry.ContainerName()
		configSet[name] = true

		id := config.NodeID("containers", name)
		hash := containerHash(entry, name)
		r.nodeConfigs[id] = entry

		action := engine.DetermineAction(id, knowledge[id], hash, st)

		level := entry.Level
		if level == "" {
			level = "common"
		}
		action, levelDesc := engine.CheckLevelChange(id, level, action, st)

		desc := describeContainerChange(entry, action)
		if levelDesc != "" {
			desc = levelDesc
		}

		rt := resolveRuntime(entry.Runtime)

		deps := entry.DependsOnIDs()
		deps = append(deps, config.NodeID("packages", rt))

		nodes = append(nodes, &engine.PlanNode{
			ID:           id,
			ResourceType: "containers",
			DisplayName:  name,
			Action:       action,
			ConfigHash:   hash,
			DependsOn:    deps,
			NeedsSudo:    entry.Sudo,
			Level:        level,
			Description:  desc,
			StateData: map[string]interface{}{
				"name":    name,
				"image":   entry.Image,
				"runtime": rt,
			},
		})
	}

	nodes = append(nodes, engine.StateRemovals("containers", configSet, knowledge, st)...)
	return nodes, nil
}

func describeContainerChange(entry *config.ContainerEntry, action engine.ActionType) string {
	rt := resolveRuntime(entry.Runtime)
	switch action {
	case engine.ActionAdd:
		return "create and start via " + rt
	case engine.ActionUpdate:
		return "recreate via " + rt
	case engine.ActionRemove:
		return "stop and remove via " + rt
	}
	return ""
}
