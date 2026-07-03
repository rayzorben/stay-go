package flatpak

import (
	"context"
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
)

// BuildPlan computes PlanNodes for all managed Flatpak remotes and apps.
// Implements engine.Planner.
func (r *Resource) BuildPlan(_ context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	configSet := make(map[string]bool)
	var nodes []*engine.PlanNode

	// Build a quick lookup of declared remotes for implicit dep injection.
	declaredRemotes := make(map[string]bool, len(r.cfg.Flatpak.Remotes))
	for _, remote := range r.cfg.Flatpak.Remotes {
		declaredRemotes[remote.Name] = true
	}

	// ── Remotes ───────────────────────────────────────────────────────────────
	for _, remote := range r.cfg.Flatpak.Remotes {
		id := remoteNodeID(remote.Name)
		configSet["remote/"+remote.Name] = true
		hash := config.Hash(remote)

		level := remote.SourceFile
		if level == "" {
			level = "common"
		}
		action := engine.DetermineAction(id, knowledge[id], hash, st)
		action, levelDesc := engine.CheckLevelChange(id, level, action, st)

		desc := remote.URL
		if levelDesc != "" {
			desc = levelDesc
		}

		nodes = append(nodes, &engine.PlanNode{
			ID:           id,
			ResourceType: "flatpak",
			DisplayName:  remote.Name,
			Action:       action,
			ConfigHash:   hash,
			SourceFile: level,
			NeedsSudo:    false,
			DependsOn:    []string{"packages/flatpak"},
			Description:  desc,
			StateData:    map[string]interface{}{"name": remote.Name, "url": remote.URL},
		})
	}

	// ── Apps ──────────────────────────────────────────────────────────────────
	for _, app := range r.cfg.Flatpak.Apps {
		remote := app.Remote
		if remote == "" {
			remote = "flathub"
		}
		id := appNodeID(app.AppID)
		configSet["app/"+app.AppID] = true
		hash := config.Hash(struct {
			AppID  string
			Remote string
		}{app.AppID, remote})

		level := app.SourceFile
		if level == "" {
			level = "common"
		}
		action := engine.DetermineAction(id, knowledge[id], hash, st)
		action, levelDesc := engine.CheckLevelChange(id, level, action, st)

		desc := "via " + remote
		if levelDesc != "" {
			desc = levelDesc
		}

		// Add the remote as an implicit dep only when it is also declared in
		// config. This avoids spurious SKIPs when the remote was configured
		// out-of-band (e.g. system-wide during initial flatpak setup).
		deps := []string{"packages/flatpak"}
		if declaredRemotes[remote] {
			deps = append(deps, remoteNodeID(remote))
		}
		deps = append(deps, app.DependsOnIDs()...)

		nodes = append(nodes, &engine.PlanNode{
			ID:           id,
			ResourceType: "flatpak",
			DisplayName:  app.AppID,
			Action:       action,
			ConfigHash:   hash,
			SourceFile: level,
			NeedsSudo:    false,
			DependsOn:    deps,
			Description:  desc,
			StateData:    map[string]interface{}{"app": app.AppID, "remote": remote},
		})
	}

	// ── Removals ──────────────────────────────────────────────────────────────
	for _, n := range engine.StateRemovals("flatpak", configSet, knowledge, st) {
		if isRemoteNode(n.ID) {
			n.DisplayName = strings.TrimPrefix(n.ID, remotePrefix)
			n.Description = "remove remote"
		} else {
			n.DisplayName = strings.TrimPrefix(n.ID, appPrefix)
			n.Description = "remove app"
		}
		nodes = append(nodes, n)
	}

	return nodes, nil
}
