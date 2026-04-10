package packages

import (
	"context"
	"sort"
	"strings"

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

	// Index sync (e.g. apt-get update): one shared node per unique dependency set.
	// Packages that share the same deps (including no deps) share a single sync
	// node, so apt-get update runs at most once per "level" of the dep graph rather
	// than once per package. The sync node is Hidden so it doesn't clutter output.
	needsSync := r.manager != nil && r.manager.UpdateCmd != nil
	// syncByKey maps a sorted dep fingerprint to the already-created sync node ID.
	syncByKey := make(map[string]string)

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
			// Build a stable fingerprint from the sorted package-level deps.
			// Packages that share the same dep set share one sync node.
			syncDeps := append([]string(nil), p.DependsOnIDs()...)
			sort.Strings(syncDeps)
			depsKey := strings.Join(syncDeps, "\x00")

			sid, exists := syncByKey[depsKey]
			if !exists {
				sid = packageSyncGroupID(depsKey)
				syncByKey[depsKey] = sid
				nodes = append(nodes, &engine.PlanNode{
					ID:           sid,
					ResourceType: "packages",
					DisplayName:  r.manager.Name + " update",
					Action:       engine.ActionAdd,
					ConfigHash:   config.Hash(r.manager.UpdateCmd),
					NeedsSudo:    r.manager.NeedsSudo,
					DependsOn:    syncDeps,
					Description:  "sync package index",
					Hidden:       true,
				})
			}
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
	// Exclude sync node IDs — they are ephemeral and must never appear in state.
	removals := engine.StateRemovals("packages", configSet, knowledge, st)
	for _, n := range removals {
		if isPackageSyncNode(n.ID) {
			continue
		}
		n.Description = describePackageChange(engine.ActionRemove, r.manager)
		nodes = append(nodes, n)
	}
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
