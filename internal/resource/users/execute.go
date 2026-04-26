package users

import (
	"context"
	"fmt"
	"strings"

	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// Execute creates, modifies, or deletes a system user.
// State is updated on success.
// Implements engine.NodeExecutor.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	name := node.DisplayName

	switch node.Action {
	case engine.ActionAdd:
		entry, ok := r.nodeConfigs[node.ID]
		if !ok {
			return fmt.Errorf("no config found for user %q", name)
		}
		args := []string{"-m"} // create home directory
		if entry.Name != "" {
			args = append(args, "-c", entry.Name) // GECOS / full name
		}
		if entry.Shell != "" {
			args = append(args, "-s", entry.Shell)
		}
		if entry.Home != "" {
			args = append(args, "-d", entry.Home)
		}
		if entry.UID != "" {
			args = append(args, "-u", entry.UID)
		}
		if len(entry.Groups) > 0 {
			args = append(args, "-G", strings.Join(entry.Groups, ","))
		}
		args = append(args, name)
		result, err := r.exec.Run(ctx, executor.Options{Sudo: true}, "useradd", args...)
		if err != nil {
			return fmt.Errorf("creating user %q: %w\nstderr: %s", name, err, result.Stderr)
		}
		st.Set(node.ID, node.ConfigHash, node.Level, node.StateData)

	case engine.ActionUpdate:
		entry, ok := r.nodeConfigs[node.ID]
		if !ok {
			return fmt.Errorf("no config found for user %q", name)
		}

		sysGroupMembers, err := parseGroupMembers()
		if err != nil {
			return fmt.Errorf("reading /etc/group: %w", err)
		}

		desiredGroupSet := make(map[string]bool, len(entry.Groups))
		for _, g := range entry.Groups {
			desiredGroupSet[g] = true
		}

		// Collect groups to add (in desired set but user not yet a member).
		var groupsToAdd []string
		for _, g := range entry.Groups {
			if members, ok := sysGroupMembers[g]; !ok || !members[name] {
				groupsToAdd = append(groupsToAdd, g)
			}
		}

		// Collect managed groups to remove (managed but not in desired set).
		var groupsToRemove []string
		for _, mg := range r.cfg.Groups {
			if !desiredGroupSet[mg.Name] {
				if members, ok := sysGroupMembers[mg.Name]; ok && members[name] {
					groupsToRemove = append(groupsToRemove, mg.Name)
				}
			}
		}

		var args []string
		if entry.Name != "" {
			args = append(args, "-c", entry.Name) // GECOS / full name
		}
		if entry.Shell != "" {
			args = append(args, "-s", entry.Shell)
		}
		if entry.Home != "" {
			args = append(args, "-d", entry.Home)
		}
		if len(groupsToAdd) > 0 {
			args = append(args, "-aG", strings.Join(groupsToAdd, ","))
		}

		if len(args) == 0 && len(groupsToRemove) == 0 {
			st.Set(node.ID, node.ConfigHash, node.Level, node.StateData)
			return nil
		}
		if len(args) > 0 {
			result, err := r.exec.Run(ctx, executor.Options{Sudo: true}, "usermod", append(args, name)...)
			if err != nil {
				return fmt.Errorf("modifying user %q: %w\nstderr: %s", name, err, result.Stderr)
			}
		}
		for _, g := range groupsToRemove {
			result, err := r.exec.Run(ctx, executor.Options{Sudo: true}, "gpasswd", "-d", name, g)
			if err != nil {
				return fmt.Errorf("removing user %q from group %q: %w\nstderr: %s", name, g, err, result.Stderr)
			}
		}
		st.Set(node.ID, node.ConfigHash, node.Level, node.StateData)

	case engine.ActionRemove:
		result, err := r.exec.Run(ctx, executor.Options{Sudo: true}, "userdel", "-r", name)
		if err != nil {
			return fmt.Errorf("deleting user %q: %w\nstderr: %s", name, err, result.Stderr)
		}
		st.Delete(node.ID)
	}

	return nil
}
