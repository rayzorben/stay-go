package packages

import (
	"context"
	"fmt"
	"sort"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// Execute installs, updates, or removes a single package.
// State is updated on success.
// Implements engine.NodeExecutor.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	if err := r.ensureManager(ctx); err != nil {
		return err
	}
	name := node.DisplayName
	opts := executor.Options{Sudo: r.manager.NeedsSudo, Env: r.manager.Env}

	switch node.Action {
	case engine.ActionUpgrade:
		return r.executeUpgrade(ctx)

	case engine.ActionAdd, engine.ActionUpdate:
		if isPackageSyncNode(node.ID) {
			// Sync nodes are ephemeral — never persisted to state.
			// They are always re-emitted as ADD so tracking them would cause
			// false removals on subsequent runs.
			if r.manager.UpdateCmd == nil {
				return nil
			}
			syncOpts := executor.Options{Sudo: r.manager.NeedsSudo, Env: r.manager.Env}
			cmd := r.manager.UpdateCmd
			result, err := r.exec.Run(ctx, syncOpts, cmd[0], cmd[1:]...)
			if err != nil {
				return fmt.Errorf("syncing package index: %w\nstderr: %s", err, result.Stderr)
			}
			return nil
		}
		cmd := r.manager.InstallCmd
		// For managers that run a separate index sync (apt-get, zypper, apk, xbps),
		// stream the install output so the user sees live progress rather than silence.
		installOpts := executor.Options{
			Sudo:   r.manager.NeedsSudo,
			Env:    r.manager.Env,
			Stream: r.manager.UpdateCmd != nil,
		}
		result, err := r.exec.Run(ctx, installOpts, cmd[0], append(cmd[1:], name)...)
		if err != nil {
			return fmt.Errorf("installing package %q: %w\nstderr: %s", name, err, result.Stderr)
		}
		// If a higher-priority manager (e.g. paru) was just installed, reset the
		// cached manager so subsequent packages use it instead of the current one.
		if betterManagerNowAvailable(r.manager) {
			r.manager = nil
		}
		st.Set(node.ID, node.ConfigHash, node.SourceFile, node.StateData)

	case engine.ActionRemove:
		if err := r.protectConfigured(ctx, name); err != nil {
			return fmt.Errorf("removing package %q: %w", name, err)
		}
		cmd := r.manager.RemoveCmd
		result, err := r.exec.Run(ctx, opts, cmd[0], append(cmd[1:], name)...)
		if err != nil {
			return fmt.Errorf("removing package %q: %w\nstderr: %s", name, err, result.Stderr)
		}
		st.Delete(node.ID)
	}

	return nil
}

// protectConfigured marks every still-configured, installed package as
// explicitly installed before a removal, so the manager's dependency cleanup
// (pacman -Rs, apt --autoremove, apk del) cannot take a desired package that
// happened to be first installed as a dependency of the one being removed.
// A failure aborts the removal rather than risk uninstalling desired packages.
func (r *Resource) protectConfigured(ctx context.Context, removing string) error {
	if r.manager.MarkExplicitCmd == nil {
		return nil
	}
	names := protectedPackages(r.cfg.Packages, r.installed, removing)
	if len(names) == 0 {
		return nil
	}
	if r.manager.ResolveCmd != nil {
		cmd := r.manager.ResolveCmd
		// A non-zero exit only means some names were not found; the
		// resolved ones are still on stdout.
		result, _ := r.exec.Run(ctx, executor.Options{Env: r.manager.Env},
			cmd[0], append(cmd[1:], names...)...)
		names = resolvedNames(result.Stdout, removing)
		if len(names) == 0 {
			return nil
		}
	}
	cmd := r.manager.MarkExplicitCmd
	opts := executor.Options{Sudo: r.manager.NeedsSudo, Env: r.manager.Env}
	result, err := r.exec.Run(ctx, opts, cmd[0], append(cmd[1:], names...)...)
	if err != nil {
		return fmt.Errorf("marking configured packages explicit: %w\nstderr: %s", err, result.Stderr)
	}
	return nil
}

// protectedPackages returns the sorted, de-duplicated names of configured
// (non-removal) packages present in installed, excluding removing itself.
func protectedPackages(pkgs []config.PackageEntry, installed map[string]bool, removing string) []string {
	seen := make(map[string]bool)
	var names []string
	for _, p := range pkgs {
		if p.Remove || p.Name == removing || seen[p.Name] {
			continue
		}
		if !installed[config.NodeID("packages", p.Name)] {
			continue
		}
		seen[p.Name] = true
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return names
}

// resolvedNames parses ResolveCmd output into unique database names,
// dropping removing so a provides match can never protect the target itself.
func resolvedNames(raw, removing string) []string {
	seen := make(map[string]bool)
	var names []string
	for _, n := range splitLines(raw) {
		if n == removing || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	return names
}
