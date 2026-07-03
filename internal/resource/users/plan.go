package users

import (
	"context"
	"fmt"
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
)

// BuildPlan computes PlanNodes for managed users.
// Uses engine.DetermineAction for base action logic (DRY).
// For users not yet tracked by stay-go, it also compares the live shell/home
// against config — if they differ, UPDATE is needed even on first tracking.
// Implements engine.Planner.
func (r *Resource) BuildPlan(_ context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	sysUsers, err := parsePasswd()
	if err != nil {
		return nil, fmt.Errorf("reading /etc/passwd: %w", err)
	}

	// Build a group→members map from /etc/group for membership checks.
	sysGroupMembers, err := parseGroupMembers()
	if err != nil {
		return nil, fmt.Errorf("reading /etc/group: %w", err)
	}

	// Collect the names of all groups managed by stay-go.
	managedGroupNames := make([]string, len(r.cfg.Groups))
	for i, g := range r.cfg.Groups {
		managedGroupNames[i] = g.Name
	}

	configSet := make(map[string]bool, len(r.cfg.Users))
	var nodes []*engine.PlanNode

	for i := range r.cfg.Users {
		entry := &r.cfg.Users[i]
		configSet[entry.Username] = true

		id := config.NodeID("users", entry.Username)
		hash := userHash(entry)
		r.nodeConfigs[id] = entry

		action := engine.DetermineAction(id, knowledge[id], hash, st)

		// DetermineAction returns ADOPT for "present but not yet in state".
		// For users, we must verify the live system actually matches config;
		// if not, an UPDATE is required to bring it into alignment.
		sys := sysUsers[entry.Username]
		if action == engine.ActionAdopt {
			if !sysUserMatchesConfig(sys, entry, sysGroupMembers, managedGroupNames) {
				action = engine.ActionUpdate
			}
		}

		// Auto-dependency: each group in the user's groups list must be
		// tracked/added before the user can be assigned to it.
		var dependsOn []string
		for _, g := range entry.Groups {
			dependsOn = append(dependsOn, config.NodeID("groups", g))
		}

		level := entry.SourceFile
		if level == "" {
			level = "common"
		}
		action, levelDesc := engine.CheckLevelChange(id, level, action, st)

		// For UPDATE nodes, diff against previously stored state data (what stay-go
		// last applied) rather than live system state. This avoids showing fields
		// that were already tracked as if they are changing.
		var desc string
		if action == engine.ActionUpdate {
			if stateEntry, ok := st.Get(id); ok && stateEntry.Data != nil {
				desc = userDiffFromState(stateEntry.Data, entry)
			} else {
				desc = describeUserChange(sys, entry, action, sysGroupMembers, managedGroupNames)
			}
		} else {
			desc = describeUserChange(sys, entry, action, sysGroupMembers, managedGroupNames)
		}
		if levelDesc != "" {
			desc = levelDesc
		}

		stateData := map[string]interface{}{
			"username": entry.Username,
			"name":     entry.Name,
			"shell":    entry.Shell,
			"home":     entry.Home,
			"uid":      entry.UID,
			"groups":   entry.Groups,
		}

		nodes = append(nodes, &engine.PlanNode{
			ID:           id,
			ResourceType: "users",
			DisplayName:  entry.Username,
			Action:       action,
			ConfigHash:   hash,
			DependsOn:    dependsOn,
			NeedsSudo:    true,
			SourceFile: level,
			Description:  desc,
			StateData:    stateData,
		})
	}

	// Append REMOVE nodes for users tracked in state but removed from config.
	nodes = append(nodes, engine.StateRemovals("users", configSet, knowledge, st)...)
	return nodes, nil
}

// describeUserChange returns a plain-English description of what will change
// for this user node. Returns empty string for TRACK (nothing changes).
func describeUserChange(sys systemUser, cfg *config.UserEntry, action engine.ActionType, sysGroupMembers map[string]map[string]bool, managedGroups []string) string {
	switch action {
	case engine.ActionAdd:
		var parts []string
		if cfg.Name != "" {
			parts = append(parts, "name "+cfg.Name)
		}
		if cfg.Shell != "" {
			parts = append(parts, "shell "+cfg.Shell)
		}
		if cfg.Home != "" {
			parts = append(parts, "home "+cfg.Home)
		}
		if cfg.UID != "" {
			parts = append(parts, "uid "+cfg.UID)
		}
		if len(cfg.Groups) > 0 {
			parts = append(parts, "groups: "+strings.Join(cfg.Groups, " "))
		}
		return strings.Join(parts, ", ")

	case engine.ActionUpdate:
		return userDiff(sys, cfg, sysGroupMembers, managedGroups)

	case engine.ActionRemove:
		desc := "home " + sys.HomeDir
		if sys.Shell != "" {
			desc += ", shell " + sys.Shell
		}
		return desc
	}
	return ""
}

// userDiff produces a "field: old → new" summary of what will change.
// Only fields that actually differ are included.
func userDiff(sys systemUser, cfg *config.UserEntry, sysGroupMembers map[string]map[string]bool, managedGroups []string) string {
	var parts []string
	if cfg.Name != "" && sys.GECOS != cfg.Name {
		parts = append(parts, fmt.Sprintf("name %q → %q", sys.GECOS, cfg.Name))
	}
	if cfg.Shell != "" && sys.Shell != cfg.Shell {
		parts = append(parts, fmt.Sprintf("shell %s → %s", sys.Shell, cfg.Shell))
	}
	if cfg.Home != "" && sys.HomeDir != cfg.Home {
		parts = append(parts, fmt.Sprintf("home %s → %s", sys.HomeDir, cfg.Home))
	}
	if cfg.UID != "" && sys.UID != cfg.UID {
		parts = append(parts, fmt.Sprintf("uid %s → %s", sys.UID, cfg.UID))
	}
	desiredGroupSet := make(map[string]bool, len(cfg.Groups))
	for _, g := range cfg.Groups {
		desiredGroupSet[g] = true
	}
	var groupParts []string
	for _, g := range cfg.Groups {
		if members, ok := sysGroupMembers[g]; !ok || !members[sys.Name] {
			groupParts = append(groupParts, "+"+g)
		} else {
			groupParts = append(groupParts, g)
		}
	}
	for _, g := range managedGroups {
		if !desiredGroupSet[g] {
			if members, ok := sysGroupMembers[g]; ok && members[sys.Name] {
				groupParts = append(groupParts, "-"+g)
			}
		}
	}
	if len(groupParts) > 0 {
		parts = append(parts, "groups: "+strings.Join(groupParts, " "))
	}
	return strings.Join(parts, ", ")
}

// userDiffFromState produces a "field: old → new" summary by comparing the
// new config against the previously persisted state data. This is more accurate
// than diffing against live system state because it shows only what stay-go
// itself is changing, ignoring manual system modifications.
func userDiffFromState(data map[string]interface{}, cfg *config.UserEntry) string {
	str := func(key string) string {
		v, _ := data[key].(string)
		return v
	}
	strs := func(key string) []string {
		v, ok := data[key]
		if !ok {
			return nil
		}
		switch tv := v.(type) {
		case []interface{}:
			out := make([]string, 0, len(tv))
			for _, item := range tv {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
			return out
		case []string:
			return tv
		}
		return nil
	}

	var parts []string
	if cfg.Name != "" && str("name") != cfg.Name {
		parts = append(parts, fmt.Sprintf("name %q → %q", str("name"), cfg.Name))
	}
	if cfg.Shell != "" && str("shell") != cfg.Shell {
		parts = append(parts, fmt.Sprintf("shell %s → %s", str("shell"), cfg.Shell))
	}
	if cfg.Home != "" && str("home") != cfg.Home {
		parts = append(parts, fmt.Sprintf("home %s → %s", str("home"), cfg.Home))
	}
	if cfg.UID != "" && str("uid") != cfg.UID {
		parts = append(parts, fmt.Sprintf("uid %s → %s", str("uid"), cfg.UID))
	}

	prevGroups := make(map[string]bool)
	for _, g := range strs("groups") {
		prevGroups[g] = true
	}
	desiredGroups := make(map[string]bool)
	for _, g := range cfg.Groups {
		desiredGroups[g] = true
	}
	var groupParts []string
	for _, g := range cfg.Groups {
		if !prevGroups[g] {
			groupParts = append(groupParts, "+"+g)
		}
	}
	for g := range prevGroups {
		if !desiredGroups[g] {
			groupParts = append(groupParts, "-"+g)
		}
	}
	if len(groupParts) > 0 {
		parts = append(parts, "groups: "+strings.Join(groupParts, " "))
	}
	return strings.Join(parts, ", ")
}
