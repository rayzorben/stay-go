package distrobox

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
)

// BuildPlan produces up to two PlanNodes per configured distrobox entry:
//
//  1. A host-level box node ("distrobox/<name>") that creates, updates,
//     or removes the container itself.
//
//  2. An in-box apply node ("distrobox/<name>/apply") that runs stay-go
//     inside the box to apply packages and commands declared under the entry.
//     This node is only emitted when the entry has in-box resources.
//     It always depends on the host-level box node.
//
// Implements engine.Planner.
func (r *Resource) BuildPlan(ctx context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	var nodes []*engine.PlanNode
	configSet := make(map[string]bool, len(r.cfg.Distrobox))

	for i := range r.cfg.Distrobox {
		entry := &r.cfg.Distrobox[i]
		configSet[entry.Name] = true
		id := boxNodeID(entry.Name)

		// ── Host-level box node ────────────────────────────────────────────────
		hash := distroboxHash(entry)
		action := engine.DetermineAction(id, knowledge[id], hash, st)
		action, levelDesc := engine.CheckLevelChange(id, entry.Level, action, st)

		deps := entry.DependsOnIDs()
		deps = append(deps, config.NodeID("packages", "distrobox"))

		boxNode := &engine.PlanNode{
			ID:           id,
			ResourceType: "distrobox",
			DisplayName:  entry.Name,
			Action:       action,
			ConfigHash:   hash,
			DependsOn:    deps,
			Level:        entry.Level,
			NeedsSudo:    needsHomeSudo(entry),
			Description:  describeBoxAction(action, entry, levelDesc),
			StateData:    map[string]interface{}{"name": entry.Name, "image": entry.Image},
		}
		nodes = append(nodes, boxNode)
		r.nodeConfigs[id] = entry

		// ── In-box apply node ─────────────────────────────────────────────────
		// Only emit when there is something to manage inside the box.
		if len(entry.Packages) == 0 && len(entry.Commands) == 0 && len(entry.Exports) == 0 {
			continue
		}

		// Install the running host binary into ~/.local/bin once per in-box plan
		// so hash-only UPDATEs still refresh the guest copy before apply or tests.
		guestBin, guestBinErr := r.ensureGuestBinary()

		aid := applyNodeID(entry.Name)
		applyHash := inBoxHash(entry)
		boxSt, _ := state.Load(r.boxStatePath(entry.Name))
		// Guest state on disk can outlive the container (manual removal). When the
		// host will create or recreate the box, plan deltas must not trust it.
		if action == engine.ActionAdd || action == engine.ActionUpdate {
			h, _ := os.Hostname()
			boxSt = &state.State{Hostname: h, Nodes: make(map[string]state.NodeEntry)}
		}

		var applyAction engine.ActionType
		var applyDesc string
		var applyNotes []string

		switch action {
		case engine.ActionAdd:
			applyAction = engine.ActionAdd
			deltas := r.guestWorkDeltas(entry, nil, false, boxSt)
			applyNotes = formatGuestWorkNotes(entry, deltas, len(entry.Packages) == 0, true, engine.ActionAdd, nil)

		case engine.ActionUpdate:
			applyAction = engine.ActionAdd
			applyDesc = "box recreated"
			deltas := r.guestWorkDeltas(entry, nil, false, boxSt)
			applyNotes = formatGuestWorkNotes(entry, deltas, len(entry.Packages) == 0, true, engine.ActionAdd, nil)

		case engine.ActionRemove:
			continue

		default:
			guestPkg, guestOk := r.guestPkgKnowledge(ctx, entry, guestBin, guestBinErr)
			applyAction = engine.DetermineAction(aid, knowledge[id], applyHash, st)
			applyAction, _ = engine.CheckLevelChange(aid, entry.Level, applyAction, st)
			if applyAction == engine.ActionAdopt {
				applyAction = engine.ActionAdd
				applyDesc = summarizeInBoxConfig(entry)
			}
			deltas := r.guestWorkDeltas(entry, guestPkg, guestOk, boxSt)
			if applyAction == engine.ActionTrack && guestWorkPending(deltas) {
				applyAction = engine.ActionUpdate
			}
			// Inverse: UPDATE triggered purely by a config hash change (e.g. a
			// package added to config that was already installed and adopted into
			// boxSt on a previous run). If guest inventory is reliable and nothing
			// is actually pending, downgrade to TRACK so the engine just saves the
			// new hash silently instead of showing an unexplained "~ update".
			if applyAction == engine.ActionUpdate && guestOk && !guestWorkPending(deltas) {
				applyAction = engine.ActionTrack
			}
			includeExports := applyAction == engine.ActionAdd || applyAction == engine.ActionUpdate
			exportAct := engine.ActionUpdate
			if applyAction == engine.ActionAdd {
				exportAct = engine.ActionAdd
			}
			var prevExports []string
			if prev, ok := st.Get(aid); ok {
				prevExports = exportsSnapshotFromStateData(prev.Data)
			}
			applyNotes = formatGuestWorkNotes(entry, deltas, guestOk, includeExports, exportAct, prevExports)
		}

		stateData := map[string]interface{}{"name": entry.Name}
		if prev, ok := st.Get(aid); ok && prev.Data != nil {
			if snap, ok := prev.Data["exports_snapshot"]; ok {
				stateData["exports_snapshot"] = snap
			}
		}

		applyNode := &engine.PlanNode{
			ID:           aid,
			ResourceType: "distrobox",
			DisplayName:  entry.Name + " (in-box)",
			Action:       applyAction,
			ConfigHash:   applyHash,
			DependsOn:    []string{id},
			Level:        entry.Level,
			Description:  applyDesc,
			Notes:        applyNotes,
			StateData: stateData,
		}
		nodes = append(nodes, applyNode)
		r.nodeConfigs[aid] = entry
	}

	// ── Removal detection ─────────────────────────────────────────────────────
	// Only match top-level box entries (format "distrobox/<name>", one slash only).
	// The "/apply" sub-nodes are cleaned up by executeBox on removal.
	for id := range st.Nodes {
		if !strings.HasPrefix(id, "distrobox/") {
			continue
		}
		name := strings.TrimPrefix(id, "distrobox/")
		if strings.Contains(name, "/") {
			continue // skip "distrobox/<name>/apply" entries
		}
		if !configSet[name] {
			nodes = append(nodes, &engine.PlanNode{
				ID:           id,
				ResourceType: "distrobox",
				DisplayName:  name,
				Action:       engine.ActionRemove,
			})
			// Store a stub entry so Execute can resolve the box name from the node ID.
			// The actual entry no longer exists in config; we synthesise one.
			r.nodeConfigs[id] = &config.DistroboxEntry{Name: name}
		}
	}

	return nodes, nil
}

// ─── Description helpers ─────────────────────────────────────────────────────

func describeBoxAction(action engine.ActionType, entry *config.DistroboxEntry, levelDesc string) string {
	switch action {
	case engine.ActionAdd:
		return fmt.Sprintf("create from %s", entry.Image)
	case engine.ActionUpdate:
		return fmt.Sprintf("recreate (config changed): %s", entry.Image)
	case engine.ActionLevel:
		return levelDesc
	default:
		return ""
	}
}

func summarizeInBoxConfig(entry *config.DistroboxEntry) string {
	var parts []string
	if n := len(entry.Packages); n > 0 {
		parts = append(parts, fmt.Sprintf("%d package(s)", n))
	}
	if n := len(entry.Commands); n > 0 {
		parts = append(parts, fmt.Sprintf("%d command(s)", n))
	}
	if n := len(entry.Exports); n > 0 {
		parts = append(parts, fmt.Sprintf("%d export(s)", n))
	}
	return strings.Join(parts, ", ")
}
