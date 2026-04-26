package commands

import (
	"context"
	"os"
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
)

// BuildPlan computes PlanNodes for managed commands.
//
// Hash: SHA-256 of (name + sudo + command + rollback).
// Any change to the inline command or rollback triggers UPDATE (re-run).
//
// File conditions: checked at plan time.
//   - "!/path" → skip if path already exists
//   - "/path"  → skip if path does not exist
//
// Implements engine.Planner.
func (r *Resource) BuildPlan(_ context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	configSet := make(map[string]bool, len(r.cfg.Commands))
	var nodes []*engine.PlanNode

	for i := range r.cfg.Commands {
		entry := &r.cfg.Commands[i]
		configSet[entry.Name] = true

		id := config.NodeID("commands", entry.Name)
		hash := config.Hash(struct {
			Name     string
			Sudo     bool
			Command  string
			Rollback string
		}{entry.Name, entry.Sudo, entry.Command, entry.Rollback})

		level := entry.Level
		if level == "" {
			level = "common"
		}

		action := engine.DetermineAction(id, knowledge[id], hash, st)
		// ADOPT means "in knowledge but not in state" — for commands this is the
		// first-time run case: promote to ADD so the command actually executes.
		if action == engine.ActionAdopt {
			action = engine.ActionAdd
		}

		action, levelDesc := engine.CheckLevelChange(id, level, action, st)

		// Evaluate file and folder conditions. Any failing condition skips the node.
		skipReason := commandPlanSkipReason(entry)

		desc := describeCommandChange(action)
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
			ResourceType: "commands",
			DisplayName:  entry.Name,
			Action:       action,
			ConfigHash:   hash,
			DependsOn:    entry.DependsOnIDs(),
			NeedsSudo:    entry.Sudo,
			Level:        level,
			Description:  desc,
			SkipReason:   skipReason,
			StateData: map[string]interface{}{
				"name":     entry.Name,
				"sudo":     entry.Sudo,
				"command":  entry.Command,
				"rollback": entry.Rollback,
			},
		})
	}

	nodes = append(nodes, engine.StateRemovals("commands", configSet, knowledge, st)...)
	return nodes, nil
}

// checkFileConditions evaluates all files: deps and returns a skip reason
// if any condition is not satisfied. Empty string means all conditions pass.
// FileConditionSkipReason returns a skip reason when a depends.files condition
// fails, or empty string when all pass.
func FileConditionSkipReason(entry *config.CommandEntry) string {
	return checkFileConditions(entry)
}

// CommandPlanSkipReason returns a skip reason from depends.files and depends.folders
// (same semantics as scripts), or empty when all pass. Exported for distrobox plan notes.
func CommandPlanSkipReason(entry *config.CommandEntry) string {
	return commandPlanSkipReason(entry)
}

func commandPlanSkipReason(entry *config.CommandEntry) string {
	if r := checkFileConditions(entry); r != "" {
		return r
	}
	return checkFolderConditions(entry)
}

func checkFileConditions(entry *config.CommandEntry) string {
	var reasons []string
	for _, path := range entry.FileConditions() {
		negate := strings.HasPrefix(path, "!")
		p := strings.TrimPrefix(path, "!")
		_, err := os.Stat(p)
		exists := err == nil
		if negate && exists {
			reasons = append(reasons, "file exists: "+p)
		} else if !negate && !exists {
			reasons = append(reasons, "file absent: "+p)
		}
	}
	return strings.Join(reasons, "; ")
}

// checkFolderConditions mirrors scripts: paths come from depends.folders after var expansion.
func checkFolderConditions(entry *config.CommandEntry) string {
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

func describeCommandChange(action engine.ActionType) string {
	switch action {
	case engine.ActionAdd:
		return "run command"
	case engine.ActionUpdate:
		return "command changed, re-run"
	case engine.ActionRemove:
		return "run rollback, remove from tracking"
	}
	return ""
}
