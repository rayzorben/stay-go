package distrobox

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/executor"
	"github.com/rayben/stay-go/internal/state"
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

		boxNode := &engine.PlanNode{
			ID:           id,
			ResourceType: "distrobox",
			DisplayName:  entry.Name,
			Action:       action,
			ConfigHash:   hash,
			DependsOn:    entry.DependsOnIDs(),
			Level:        entry.Level,
			NeedsSudo:    entry.HomeSudo,
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

		aid := applyNodeID(entry.Name)
		applyHash := inBoxHash(entry)

		var applyAction engine.ActionType
		var applyDesc string
		var applyNotes []string

		switch action {
		case engine.ActionAdd:
			// Box is being created — all in-box resources are new.
			applyAction = engine.ActionAdd
			applyDesc = summarizeInBoxConfig(entry)
			applyNotes = buildApplyNotes(entry)

		case engine.ActionUpdate:
			// Box is being recreated — in-box state effectively resets.
			applyAction = engine.ActionAdd
			applyDesc = "box recreated: " + summarizeInBoxConfig(entry)
			applyNotes = buildApplyNotes(entry)

		case engine.ActionRemove:
			// Box is going away — no in-box apply needed.
			continue

		default:
			// Box exists and is stable: determine apply action normally.
			// The apply node is always "in knowledge" when the box exists.
			applyAction = engine.DetermineAction(aid, knowledge[id], applyHash, st)
			applyAction, _ = engine.CheckLevelChange(aid, entry.Level, applyAction, st)
			if applyAction == engine.ActionAdopt {
				applyAction = engine.ActionAdd
				applyDesc = summarizeInBoxConfig(entry)
				applyNotes = buildApplyNotes(entry)
			}

				// Missing-resource check: even if the config hash is unchanged,
			// some resources inside the box may not be applied (failed install,
			// manual removal, etc.). Treat any missing item as ADD — the same
			// logic the engine uses for the host (NO/YES/YES → ADD).
			if applyAction == engine.ActionTrack {
				missingPkgs, missingCmds := r.detectMissing(ctx, entry)
				if len(missingPkgs) > 0 || len(missingCmds) > 0 {
					applyAction = engine.ActionUpdate
					// No description — the notes below show the specifics.
					var notes []string
					if len(missingPkgs) > 0 {
						items := make([]string, len(missingPkgs))
						for i, p := range missingPkgs {
							items[i] = "+" + p
						}
						notes = append(notes, formatNoteList("packages", items))
					}
					if len(missingCmds) > 0 {
						items := make([]string, len(missingCmds))
						for i, c := range missingCmds {
							items[i] = "+" + c
						}
						notes = append(notes, "commands: "+strings.Join(items, ", "))
					}
					applyNotes = notes
				}
			}

			// Config hash changed (not drift): show only what changed vs box state.
			if applyAction == engine.ActionUpdate && applyNotes == nil {
				applyNotes = r.diffApplyNotes(entry)
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
			StateData:    map[string]interface{}{"name": entry.Name},
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

// ─── Missing-resource detection ───────────────────────────────────────────────

// detectMissing checks whether configured in-box resources are actually present,
// applying the same KNOWLEDGE/STATE/CONFIG matrix the engine uses on the host:
//
//	NO / YES / YES → ADD   (not installed / not run, but in config and state)
//
// Packages are checked via --guest-knowledge (what is installed inside the box).
// Commands are checked via the per-box state file (has the command ever run).
//
// Errors are non-fatal: a failed check is treated as "nothing missing" so that
// transient issues (box stopped, binary absent) do not manufacture spurious work.
func (r *Resource) detectMissing(ctx context.Context, entry *config.DistroboxEntry) (missingPkgs []string, missingCmds []string) {
	// ── Packages: query the box for what is installed ─────────────────────────
	if len(entry.Packages) > 0 {
		if err := r.writeBoxConfig(entry); err == nil {
			if guestBin, err := r.ensureGuestBinary(); err == nil {
				result, err := r.exec.Run(ctx, executor.Options{},
					"distrobox", "enter", "-n", entry.Name, "--",
					guestBin, "--guest-knowledge", "-c", r.boxConfigDir(entry.Name),
				)
				if err == nil && result.ExitCode == 0 {
					var knowledge map[string]bool
					for _, line := range strings.Split(result.Stdout, "\n") {
						line = strings.TrimSpace(line)
						if strings.HasPrefix(line, "{") {
							if err := json.Unmarshal([]byte(line), &knowledge); err == nil {
								break
							}
						}
					}
					for _, pkg := range entry.Packages {
						if !knowledge["packages/"+pkg.Name] {
							missingPkgs = append(missingPkgs, pkg.Name)
						}
					}
				}
			}
		}
	}

	// ── Commands: check the per-box state file ────────────────────────────────
	if len(entry.Commands) > 0 {
		boxSt, err := state.Load(r.boxStatePath(entry.Name))
		if err != nil {
			// No box state at all — every command is pending.
			for _, cmd := range entry.Commands {
				missingCmds = append(missingCmds, cmd.Name)
			}
		} else {
			for _, cmd := range entry.Commands {
				if _, inState := boxSt.Get("commands/" + cmd.Name); !inState {
					missingCmds = append(missingCmds, cmd.Name)
				}
			}
		}
	}

	return missingPkgs, missingCmds
}

// diffApplyNotes computes targeted diff notes for an in-box UPDATE by comparing
// the current config against what the box state last tracked. Only changed
// packages and commands are shown (with + / - prefixes). Falls back to
// buildApplyNotes when box state is unavailable or the diff is empty.
func (r *Resource) diffApplyNotes(entry *config.DistroboxEntry) []string {
	boxSt, err := state.Load(r.boxStatePath(entry.Name))
	if err != nil {
		return buildApplyNotes(entry)
	}

	var notes []string

	// Package diff: what's in box state vs current config.
	prevPkgs := make(map[string]bool)
	for id := range boxSt.Nodes {
		if strings.HasPrefix(id, "packages/") {
			prevPkgs[strings.TrimPrefix(id, "packages/")] = true
		}
	}
	currPkgs := make(map[string]bool)
	for _, p := range entry.Packages {
		currPkgs[p.Name] = true
	}
	var pkgDiff []string
	for name := range prevPkgs {
		if !currPkgs[name] {
			pkgDiff = append(pkgDiff, "-"+name)
		}
	}
	for _, p := range entry.Packages {
		if !prevPkgs[p.Name] {
			pkgDiff = append(pkgDiff, "+"+p.Name)
		}
	}
	if len(pkgDiff) > 0 {
		sort.Strings(pkgDiff)
		notes = append(notes, formatNoteList("packages", pkgDiff))
	}

	// Command diff: what's in box state vs current config.
	prevCmds := make(map[string]bool)
	for id := range boxSt.Nodes {
		if strings.HasPrefix(id, "commands/") {
			prevCmds[strings.TrimPrefix(id, "commands/")] = true
		}
	}
	currCmds := make(map[string]bool)
	for _, c := range entry.Commands {
		currCmds[c.Name] = true
	}
	var cmdDiff []string
	for name := range prevCmds {
		if !currCmds[name] {
			cmdDiff = append(cmdDiff, "-"+name)
		}
	}
	for _, c := range entry.Commands {
		if !prevCmds[c.Name] {
			cmdDiff = append(cmdDiff, "+"+c.Name)
		}
	}
	if len(cmdDiff) > 0 {
		sort.Strings(cmdDiff)
		notes = append(notes, "commands: "+strings.Join(cmdDiff, ", "))
	}

	if len(notes) == 0 {
		// Hash changed but diff is empty (e.g. only exports changed) — show full summary.
		return buildApplyNotes(entry)
	}
	return notes
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
