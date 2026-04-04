package packages

import (
	"context"
	"fmt"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/state"
)

// BuildPlan computes PlanNodes for all managed packages.
// Uses engine.DetermineAction for action logic and engine.StateRemovals for
// detecting packages to remove — both shared helpers (DRY).
// Implements engine.Planner.
func (r *Resource) BuildPlan(_ context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	// configSet only includes packages we install (not forced-removal entries).
	configSet := make(map[string]bool, len(r.cfg.Packages))
	for _, p := range r.cfg.Packages {
		if !p.Remove {
			configSet[p.Name] = true
		}
	}

	var nodes []*engine.PlanNode

	// Per-package index sync (e.g. apt-get update): one node per install package
	// so ordering is always (that package's depends…) → sync → install. This
	// avoids a single global sync running before unrelated commands, and keeps
	// independent packages from waiting on another package's command deps.
	needsSync := r.manager != nil && r.manager.UpdateCmd != nil

	// Emit forced-removal ("!pkg") nodes first so they execute before installs,
	// avoiding conflicts. Only emit a node when the package is actually installed.
	for _, p := range r.cfg.Packages {
		if !p.Remove {
			continue
		}
		id := config.NodeID("packages", p.Name)
		if !knowledge[id] {
			continue // already absent — nothing to do
		}
		nodes = append(nodes, &engine.PlanNode{
			ID:           id,
			ResourceType: "packages",
			DisplayName:  p.Name,
			Action:       engine.ActionRemove,
			ConfigHash:   config.Hash(p.Name),
			NeedsSudo:    r.manager != nil && r.manager.NeedsSudo,
			Description:  describePackageChange(engine.ActionRemove, r.manager),
		})
	}

	for _, p := range r.cfg.Packages {
		if p.Remove {
			continue
		}
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

		deps := append([]string(nil), p.DependsOnIDs()...)
		if needsSync {
			sid := packageSyncID(p.Name)
			syncDeps := append([]string(nil), p.DependsOnIDs()...)
			nodes = append(nodes, &engine.PlanNode{
				ID:           sid,
				ResourceType: "packages",
				DisplayName:  fmt.Sprintf("%s update (%s)", r.manager.Name, p.Name),
				Action:       engine.ActionAdd,
				ConfigHash:   config.Hash(r.manager.UpdateCmd),
				NeedsSudo:    r.manager.NeedsSudo,
				DependsOn:    syncDeps,
				Description:  "sync package index",
			})
			deps = append(deps, sid)
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
			DependsOn:    deps,
			StateData:    map[string]interface{}{"name": p.Name},
		})
	}

	// Append REMOVE nodes for packages tracked in state but removed from config.
	removals := engine.StateRemovals("packages", configSet, knowledge, st)
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
