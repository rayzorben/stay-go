package scripts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/state"
)

// BuildPlan computes PlanNodes for managed scripts.
//
// Hash: SHA-256 of (script path + sudo flag + script file content).
// If the file content or config changes the hash changes → UPDATE (re-run).
//
// Folder conditions: checked at plan time.
//   - "!/path" → skip if path already exists
//   - "/path"  → skip if path does not exist
//
// Implements engine.Planner.
func (r *Resource) BuildPlan(_ context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	configSet := make(map[string]bool, len(r.cfg.Scripts))
	var nodes []*engine.PlanNode

	for i := range r.cfg.Scripts {
		entry := &r.cfg.Scripts[i]
		configSet[entry.Script] = true

		id := nodeID(entry.Script)
		displayName := filepath.Base(entry.Script)

		level := entry.Level
		if level == "" {
			level = "common"
		}

		// Read script file to include its content in the hash.
		content, err := os.ReadFile(entry.Script)
		if err != nil {
			nodes = append(nodes, &engine.PlanNode{
				ID:           id,
				ResourceType: "scripts",
				DisplayName:  displayName,
				Action:       engine.ActionSkip,
				SkipReason:   fmt.Sprintf("file not found: %s", entry.Script),
				Level:        level,
			})
			continue
		}

		hash := config.Hash(struct {
			Script  string
			Sudo    bool
			Content string
		}{entry.Script, entry.Sudo, string(content)})

		action := engine.DetermineAction(id, knowledge[id], hash, st)
		// ADOPT means "in knowledge but not in state" — for scripts this is the
		// first-time run case: promote to ADD so the script actually executes.
		if action == engine.ActionAdopt {
			action = engine.ActionAdd
		}

		action, levelDesc := engine.CheckLevelChange(id, level, action, st)

		// Evaluate folder conditions. Any failing condition skips the node.
		skipReason := checkFolderConditions(entry)

		desc := describeScriptChange(action)
		if levelDesc != "" {
			desc = levelDesc
		}
		if skipReason != "" {
			action = engine.ActionSkip
			desc = ""
		}

		r.nodeConfigs[id] = entry
		nodes = append(nodes, &engine.PlanNode{
			ID:           id,
			ResourceType: "scripts",
			DisplayName:  displayName,
			Action:       action,
			ConfigHash:   hash,
			DependsOn:    entry.DependsOnIDs(),
			NeedsSudo:    entry.Sudo,
			Level:        level,
			Description:  desc,
			SkipReason:   skipReason,
			StateData:    map[string]interface{}{"script": entry.Script, "sudo": entry.Sudo},
		})
	}

	nodes = append(nodes, engine.StateRemovals("scripts", configSet, st)...)
	return nodes, nil
}

// checkFolderConditions evaluates all folder: deps and returns a skip reason
// if any condition is not satisfied. Empty string means all conditions pass.
func checkFolderConditions(entry *config.ScriptEntry) string {
	var reasons []string
	for _, path := range entry.FolderConditions() {
		negate := strings.HasPrefix(path, "!")
		p := strings.TrimPrefix(path, "!")
		_, err := os.Stat(p)
		exists := err == nil
		if negate && exists {
			reasons = append(reasons, "folder exists: "+p)
		} else if !negate && !exists {
			reasons = append(reasons, "folder absent: "+p)
		}
	}
	return strings.Join(reasons, "; ")
}

func describeScriptChange(action engine.ActionType) string {
	switch action {
	case engine.ActionAdd:
		return "run script"
	case engine.ActionUpdate:
		return "script changed, re-run"
	case engine.ActionRemove:
		return "remove from tracking"
	}
	return ""
}
