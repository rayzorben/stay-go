package packages

import (
	"context"
	"fmt"
	"strings"

	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

const (
	batchInstall = "install"
	batchRemove  = "remove"
)

// BatchKey groups installs (ADD/UPDATE) and removals so the engine can hand
// each group to ExecuteBatch as one package-manager transaction. Index-sync
// and system-upgrade nodes are never batched.
// Implements engine.BatchExecutor.
func (r *Resource) BatchKey(node *engine.PlanNode) string {
	if isPackageSyncNode(node.ID) || node.ID == upgradeNodeID {
		return ""
	}
	switch node.Action {
	case engine.ActionAdd, engine.ActionUpdate:
		return batchInstall
	case engine.ActionRemove:
		return batchRemove
	}
	return ""
}

// ExecuteBatch installs or removes all nodes with a single package-manager
// invocation (one index sync, one dependency resolution, one run of the
// post-transaction hooks). Transactions are all-or-nothing, so if the batch
// fails the nodes it did not resolve are retried one at a time — the failing
// package is isolated and unrelated packages still succeed.
// Implements engine.BatchExecutor.
func (r *Resource) ExecuteBatch(ctx context.Context, nodes []*engine.PlanNode, st *state.State) []error {
	errs := make([]error, len(nodes))
	if err := r.ensureManager(ctx); err != nil {
		for i := range errs {
			errs[i] = err
		}
		return errs
	}

	remove := r.BatchKey(nodes[0]) == batchRemove
	var bootstrap, pending []int
	for i, n := range nodes {
		// A higher-priority manager (paru/yay) installed under the current one
		// must go first and alone: the remaining packages may need it (AUR).
		if !remove && r.isBetterManager(n.DisplayName) {
			bootstrap = append(bootstrap, i)
		} else {
			pending = append(pending, i)
		}
	}
	for _, i := range bootstrap {
		errs[i] = r.Execute(ctx, nodes[i], st) // resets r.manager once installed
	}
	if len(pending) == 0 {
		return errs
	}
	if err := r.ensureManager(ctx); err != nil { // re-detect if a bootstrap reset it
		for _, i := range pending {
			errs[i] = err
		}
		return errs
	}

	names := make([]string, len(pending))
	for j, i := range pending {
		names[j] = nodes[i].DisplayName
	}
	cmd := r.manager.InstallCmd
	opts := executor.Options{Sudo: r.manager.NeedsSudo, Env: r.manager.Env, Stream: r.manager.UpdateCmd != nil}
	if remove {
		cmd = r.manager.RemoveCmd
		opts.Stream = false
	}
	_, batchErr := r.exec.Run(ctx, opts, cmd[0], append(append([]string(nil), cmd[1:]...), names...)...)
	if batchErr == nil {
		for _, i := range pending {
			if remove {
				st.Delete(nodes[i].ID)
			} else {
				st.Set(nodes[i].ID, nodes[i].ConfigHash, nodes[i].SourceFile, nodes[i].StateData)
			}
		}
		if !remove && betterManagerNowAvailable(r.manager) {
			r.manager = nil
		}
		return errs
	}

	// Fallback: credit nodes the failed transaction resolved anyway (managers
	// without atomic transactions), retry the rest individually.
	present := r.installedSet(ctx)
	for _, i := range pending {
		n := nodes[i]
		if present != nil {
			switch {
			case n.Action == engine.ActionAdd && present[n.ID]:
				st.Set(n.ID, n.ConfigHash, n.SourceFile, n.StateData)
				continue
			case n.Action == engine.ActionRemove && !present[n.ID]:
				st.Delete(n.ID)
				continue
			}
		}
		if err := r.Execute(ctx, n, st); err != nil {
			errs[i] = fmt.Errorf("%w\n(batch %s of %d packages failed; retried individually)",
				err, cmd[0], len(names))
		}
	}
	return errs
}

// isBetterManager reports whether pkg installs a package manager ranked above
// the current one (e.g. "paru" or "paru-bin" while running under pacman).
func (r *Resource) isBetterManager(pkg string) bool {
	for i := range managers {
		m := &managers[i]
		if m.Name == r.manager.Name {
			return false
		}
		if pkg == m.Binary || strings.HasPrefix(pkg, m.Binary+"-") {
			return true
		}
	}
	return false
}

// installedSet returns the set of installed package node IDs, or nil if the
// system could not be queried.
func (r *Resource) installedSet(ctx context.Context) map[string]bool {
	entries, err := r.GatherKnowledge(ctx)
	if err != nil {
		return nil
	}
	present := make(map[string]bool, len(entries))
	for _, e := range entries {
		present[e.ID] = true
	}
	return present
}

var _ engine.BatchExecutor = (*Resource)(nil)
