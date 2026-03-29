package json

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/state"
)

// BuildPlan computes PlanNodes for managed JSON file value overrides.
//
// Hash: SHA-256 of (file path + desired values map).
// Any change to the desired values triggers UPDATE.
//
// On ADD/UPDATE: reads current values from the JSON file, saves originals to
// state (only on first application — originals are never overwritten), then
// applies the desired values.
//
// On REMOVE: restores the saved original values.
//
// Implements engine.Planner.
func (r *Resource) BuildPlan(_ context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	configSet := make(map[string]bool, len(r.cfg.Json))
	var nodes []*engine.PlanNode

	for i := range r.cfg.Json {
		entry := &r.cfg.Json[i]
		configSet[entry.File] = true

		id := config.NodeID("json", entry.File)
		hash := config.Hash(struct {
			File   string
			Values map[string]interface{}
		}{entry.File, entry.Values})

		level := entry.Level
		if level == "" {
			level = "common"
		}

		action := engine.DetermineAction(id, knowledge[id], hash, st)
		// ADOPT means the file exists but was never tracked — treat as ADD so
		// we read current values, persist originals, and apply desired values.
		if action == engine.ActionAdopt {
			action = engine.ActionAdd
		}

		action, levelDesc := engine.CheckLevelChange(id, level, action, st)

		// For ADD/UPDATE: read current values from disk to build the description
		// and populate originals for state (originals are only stored once).
		var originals map[string]interface{}
		var desc string
		var skipReason string

		if action == engine.ActionAdd || action == engine.ActionUpdate {
			doc, err := readFile(entry.File)
			if err != nil {
				skipReason = fmt.Sprintf("cannot read %s: %v", entry.File, err)
			} else if doc == nil {
				skipReason = fmt.Sprintf("file not found: %s", entry.File)
			} else {
				originals = collectOriginals(doc, entry.Values, st, id)
				desc = describeChanges(doc, entry.Values, action)
			}
		}

		if levelDesc != "" {
			desc = levelDesc
		}
		if skipReason != "" {
			action = engine.ActionSkip
			desc = ""
		}

		// StateData stores both the desired values and the originals.
		// Originals are never overwritten once written — the execute step
		// checks state before storing to preserve the first-seen values.
		stateData := map[string]interface{}{
			"file":   entry.File,
			"values": entry.Values,
		}
		if originals != nil {
			stateData["originals"] = originals
		}

		r.nodeConfigs[id] = entry
		nodes = append(nodes, &engine.PlanNode{
			ID:           id,
			ResourceType: "json",
			DisplayName:  entry.File,
			Action:       action,
			ConfigHash:   hash,
			DependsOn:    entry.DependsOnIDs(),
			Level:        level,
			Description:  desc,
			SkipReason:   skipReason,
			StateData:    stateData,
		})
	}

	nodes = append(nodes, engine.StateRemovals("json", configSet, st)...)
	return nodes, nil
}

// collectOriginals reads the current values for each desired path from doc,
// returning the map of path → current value. If originals are already stored
// in state (from a previous ADD), those are returned unchanged so that the
// first-seen value is preserved across updates.
func collectOriginals(doc map[string]interface{}, desired map[string]interface{}, st *state.State, id string) map[string]interface{} {
	// If state already has originals, preserve them.
	if entry, ok := st.Get(id); ok && entry.Data != nil {
		if saved, ok := entry.Data["originals"].(map[string]interface{}); ok && len(saved) > 0 {
			return saved
		}
	}
	originals := make(map[string]interface{}, len(desired))
	for path := range desired {
		if v, ok := getPath(doc, path); ok {
			originals[path] = v
		}
	}
	return originals
}

// describeChanges produces a human-readable summary of the values that will change.
func describeChanges(doc map[string]interface{}, desired map[string]interface{}, action engine.ActionType) string {
	if action == engine.ActionUpdate {
		// Summarise which paths differ from the current on-disk state.
		var diffs []string
		paths := sortedKeys(desired)
		for _, path := range paths {
			want := desired[path]
			got, exists := getPath(doc, path)
			if !exists || fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
				shortPath := strings.TrimPrefix(path, "json.")
				diffs = append(diffs, fmt.Sprintf("%s: %v → %v", shortPath, got, want))
			}
		}
		if len(diffs) > 0 {
			return strings.Join(diffs, ", ")
		}
		return "apply json overrides"
	}
	// ADD: list all paths being set.
	var parts []string
	paths := sortedKeys(desired)
	for _, path := range paths {
		shortPath := strings.TrimPrefix(path, "json.")
		parts = append(parts, fmt.Sprintf("%s = %v", shortPath, desired[path]))
	}
	return strings.Join(parts, ", ")
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
