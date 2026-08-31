package packages

import (
	"context"
	"fmt"
	"strings"

	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// upgradeNodeID is the ephemeral plan node for a full system upgrade
// (--update mode). Like sync nodes, it is never persisted to state.
const upgradeNodeID = "packages/__upgrade__"

// BuildUpdatePlan emits a single system-upgrade node. Package upgrades are
// all-or-nothing by design: partial upgrades are unsupported on Arch and of
// little value elsewhere, so there is no per-package upgrade node. Packages
// with lock: true are held back via the manager's exclusion flag when it has
// one (pacman/paru/yay --ignore, dnf/yum --exclude); otherwise the plan says
// they cannot be held.
// Implements engine.Updater.
func (r *Resource) BuildUpdatePlan(ctx context.Context, _ *state.State) ([]*engine.PlanNode, error) {
	if err := r.ensureManager(ctx); err != nil {
		return nil, err
	}
	if r.manager.UpgradeCmd == nil {
		return nil, nil
	}

	locked := r.lockedPackages()
	node := &engine.PlanNode{
		ID:           upgradeNodeID,
		ResourceType: "packages",
		DisplayName:  "system upgrade",
		Action:       engine.ActionUpgrade,
		NeedsSudo:    r.manager.NeedsSudo,
		SourceFile:   "common",
		Description:  strings.Join(upgradeArgs(r.manager, locked), " "),
	}
	if len(locked) > 0 && r.manager.IgnoreFmt == "" {
		node.Notes = append(node.Notes,
			fmt.Sprintf("%s cannot hold packages per run — locked [%s] may still be upgraded",
				r.manager.Name, strings.Join(locked, ", ")))
	}
	return []*engine.PlanNode{node}, nil
}

// lockedPackages returns the names of all configured packages with lock: true.
func (r *Resource) lockedPackages() []string {
	var locked []string
	for _, p := range r.cfg.Packages {
		if p.Lock && !p.Remove {
			locked = append(locked, p.Name)
		}
	}
	return locked
}

// upgradeArgs assembles the full upgrade command: UpgradeCmd plus one
// exclusion flag per locked package when the manager supports it.
func upgradeArgs(mgr *PackageManager, locked []string) []string {
	args := append([]string(nil), mgr.UpgradeCmd...)
	if mgr.IgnoreFmt != "" {
		for _, name := range locked {
			args = append(args, fmt.Sprintf(mgr.IgnoreFmt, name))
		}
	}
	return args
}

// executeUpgrade runs the index sync (when the manager separates it) followed
// by the full system upgrade. Output is streamed — upgrades are long-running
// and the user needs to see progress. State is never touched: an upgrade does
// not change the desired config.
func (r *Resource) executeUpgrade(ctx context.Context) error {
	if err := r.ensureManager(ctx); err != nil {
		return err
	}
	if r.manager.UpgradeCmd == nil {
		return fmt.Errorf("package manager %q does not support upgrades", r.manager.Name)
	}
	opts := executor.Options{Sudo: r.manager.NeedsSudo, Env: r.manager.Env, Stream: true}
	if r.manager.UpdateCmd != nil {
		cmd := r.manager.UpdateCmd
		result, err := r.exec.Run(ctx, opts, cmd[0], cmd[1:]...)
		if err != nil {
			return fmt.Errorf("syncing package index: %w\nstderr: %s", err, result.Stderr)
		}
	}
	args := upgradeArgs(r.manager, r.lockedPackages())
	result, err := r.exec.Run(ctx, opts, args[0], args[1:]...)
	if err != nil {
		return fmt.Errorf("upgrading packages: %w\nstderr: %s", err, result.Stderr)
	}
	return nil
}
