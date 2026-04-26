package groups

import (
	"context"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
)

// BuildPlan computes PlanNodes for managed groups.
// Uses engine.DetermineAction for standard action logic (DRY).
// Implements engine.Planner.
func (r *Resource) BuildPlan(_ context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	configSet := make(map[string]bool, len(r.cfg.Groups))
	var nodes []*engine.PlanNode

	for i := range r.cfg.Groups {
		entry := &r.cfg.Groups[i]
		configSet[entry.Name] = true

		id := config.NodeID("groups", entry.Name)
		hash := config.Hash(entry.Name)

		action := engine.DetermineAction(id, knowledge[id], hash, st)

		level := entry.Level
		if level == "" {
			level = "common"
		}
		action, levelDesc := engine.CheckLevelChange(id, level, action, st)

		desc := describeGroupChange(action)
		if levelDesc != "" {
			desc = levelDesc
		}

		nodes = append(nodes, &engine.PlanNode{
			ID:           id,
			ResourceType: "groups",
			DisplayName:  entry.Name,
			Action:       action,
			ConfigHash:   hash,
			NeedsSudo:    true,
			Level:        level,
			Description:  desc,
			StateData:    map[string]interface{}{"name": entry.Name},
		})
	}

	nodes = append(nodes, engine.StateRemovals("groups", configSet, knowledge, st)...)
	return nodes, nil
}

func describeGroupChange(action engine.ActionType) string {
	switch action {
	case engine.ActionAdd:
		return "create system group"
	case engine.ActionRemove:
		return "delete system group"
	}
	return ""
}
