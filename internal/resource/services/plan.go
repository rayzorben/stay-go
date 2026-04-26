package services

import (
	"context"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
)

// BuildPlan computes PlanNodes for managed services.
// Uses engine.DetermineAction for standard action logic (DRY).
// Dependency IDs are extracted from each service's depends field and attached
// to the plan node so the engine can enforce ordering and skip propagation.
// Implements engine.Planner.
func (r *Resource) BuildPlan(_ context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	configSet := make(map[string]bool, len(r.cfg.Services))
	var nodes []*engine.PlanNode

	for i := range r.cfg.Services {
		entry := &r.cfg.Services[i]
		configSet[entry.Service] = true

		id := config.NodeID("services", entry.Service)
		hash := serviceHash(entry)
		r.nodeConfigs[id] = entry

		inKnowledge := knowledge[id]
		desiredEnabled := entry.IsEnabled()

		// Services have an explicit enabled/disabled intent: if the user sets
		// enabled: false we treat that as a REMOVE (disable the service).
		var action engine.ActionType
		if !desiredEnabled && inKnowledge {
			action = engine.ActionRemove
		} else {
			action = engine.DetermineAction(id, inKnowledge, hash, st)
		}

		// DisplayName encodes scope: "system/sshd" or "user/pipewire".
		scope := "system"
		if entry.User {
			scope = "user"
		}

		level := entry.Level
		if level == "" {
			level = "common"
		}
		action, levelDesc := engine.CheckLevelChange(id, level, action, st)

		desc := describeServiceChange(entry, action)
		if levelDesc != "" {
			desc = levelDesc
		}

		nodes = append(nodes, &engine.PlanNode{
			ID:           id,
			ResourceType: "services",
			DisplayName:  scope + "/" + entry.Service,
			Action:       action,
			ConfigHash:   hash,
			DependsOn:    entry.DependsOnIDs(),
			NeedsSudo:    !entry.User,
			Level:        level,
			Description:  desc,
			StateData: map[string]interface{}{
				"service": entry.Service,
				"user":    entry.User,
				"enabled": entry.IsEnabled(),
			},
		})
	}

	// Append REMOVE nodes for services tracked in state but removed from config.
	nodes = append(nodes, engine.StateRemovals("services", configSet, knowledge, st)...)
	return nodes, nil
}

// describeServiceChange returns a plain-English description of what will happen
// to this service. Empty string for TRACK (nothing changes).
func describeServiceChange(entry *config.ServiceEntry, action engine.ActionType) string {
	scope := "system"
	if entry.User {
		scope = "user"
	}
	switch action {
	case engine.ActionAdd:
		return "enable and start " + scope + " service"
	case engine.ActionRemove:
		return "disable and stop " + scope + " service"
	case engine.ActionUpdate:
		return "reload and restart " + scope + " service"
	}
	return ""
}
