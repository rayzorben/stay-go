package engine

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rayzorben/stay-go/internal/state"
)

// RunUpdate executes the update pipeline (--update):
//
//  1. BuildUpdatePlan — every resource implementing Updater returns
//     ActionUpgrade nodes for its tracked items (registration order)
//  2. Target filter  — restrict to the requested resource types / nodes;
//     locked entries become SKIP unless explicitly targeted
//  3. Secrets barrier — the secrets resource's plan is included so plaintext
//     is substituted into the config before any upgrade executes
//  4. DisplayPlan + Confirm (skips always shown — locks must be visible)
//  5. Execute (shared loop) + SaveState
//
// Update mode never changes desired state: upgrade nodes refresh installed
// items to their latest version and leave the state file's hashes untouched.
func (e *Engine) RunUpdate(ctx context.Context, st *state.State, targets []string) error {
	// ── Step 1: Build upgrade nodes ───────────────────────────────────────────
	var updateNodes []*PlanNode
	for _, r := range e.resources {
		u, ok := r.(Updater)
		if !ok {
			continue
		}
		nodes, err := u.BuildUpdatePlan(ctx, st)
		if err != nil {
			return fmt.Errorf("building update plan for %q: %w", r.Type(), err)
		}
		updateNodes = append(updateNodes, nodes...)
	}

	if len(updateNodes) == 0 {
		fmt.Fprintf(os.Stdout, "\n  Nothing to update — no updatable resources tracked.\n\n")
		return nil
	}

	// ── Step 2: Target filter + lock handling ────────────────────────────────
	allNodes, err := filterUpdateNodes(updateNodes, targets)
	if err != nil {
		return err
	}
	if len(allNodes) == 0 {
		fmt.Fprintf(os.Stdout, "\n  Nothing to update — no matching resources.\n\n")
		return nil
	}

	// ── Step 3: Secrets barrier ──────────────────────────────────────────────
	// Compose projects and container env vars reference ${secrets.x}; the
	// secrets resource substitutes plaintext into the shared config during its
	// Execute, so its plan nodes must run before any upgrade node.
	for _, r := range e.resources {
		if r.Type() != "secrets" {
			continue
		}
		entries, err := r.GatherKnowledge(ctx)
		if err != nil {
			return fmt.Errorf("gathering knowledge for \"secrets\": %w", err)
		}
		knowledge := make(map[string]bool, len(entries))
		for _, entry := range entries {
			knowledge[entry.ID] = true
		}
		secretNodes, err := r.BuildPlan(ctx, knowledge, st)
		if err != nil {
			return fmt.Errorf("building plan for \"secrets\": %w", err)
		}
		allNodes = append(allNodes, secretNodes...)
		break
	}
	addSecretBarrierDeps(allNodes)

	markSkips(allNodes)

	sorted, err := topoSort(allNodes)
	if err != nil {
		return fmt.Errorf("sorting update plan: %w", err)
	}

	// ── Step 4: Display and confirm ──────────────────────────────────────────
	hasChanges := false
	for _, n := range sorted {
		if n.Action == ActionUpgrade {
			hasChanges = true
			break
		}
	}

	// Locked/unavailable entries must be visible without -S: seeing
	// "frigate … locked" is the whole point of the lock feature.
	DisplayPlan(os.Stdout, sorted, true)

	if !hasChanges {
		fmt.Fprintf(os.Stdout, "  Nothing to update.\n\n")
		return nil
	}
	if e.opts.DryRun {
		fmt.Fprintf(os.Stdout, "  %s(dry-run — no changes applied)%s\n\n", ansiDim, ansiReset)
		return nil
	}
	if !e.opts.AutoYes {
		resp, err := Confirm(os.Stdout, os.Stdin, false)
		if err != nil {
			return fmt.Errorf("reading confirmation: %w", err)
		}
		if resp != ConfirmProceed {
			fmt.Fprintf(os.Stdout, "\n  Aborted.\n\n")
			return nil
		}
	}

	// ── Step 5: Execute and save state ───────────────────────────────────────
	executeErr := e.execute(ctx, sorted, st)

	// Secrets TRACK nodes touch state (sourceFile sync); upgrades do not.
	if err := state.Save(st, e.opts.StatePath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save state: %v\n", err)
	}
	return executeErr
}

// filterUpdateNodes restricts the upgrade nodes to the requested targets and
// applies lock semantics. With no targets (or "all"), every node is included
// but Locked ones are turned into SKIP. A resource-type target ("containers")
// behaves like a bulk update of that type. A node-level target
// ("containers/frigate", "containers.frigate", or the bare name "frigate")
// deliberately names one item, so it overrides lock: true.
func filterUpdateNodes(nodes []*PlanNode, targets []string) ([]*PlanNode, error) {
	types := make(map[string]bool, 4)
	for _, n := range nodes {
		types[n.ResourceType] = true
	}

	bulk := len(targets) == 0
	bulkTypes := make(map[string]bool)
	explicit := make(map[string]bool) // node ID → explicitly targeted

	for _, raw := range targets {
		target := strings.TrimSpace(raw)
		if target == "" {
			continue
		}
		if target == "all" {
			bulk = true
			continue
		}
		if types[target] {
			bulkTypes[target] = true
			continue
		}
		ids, err := matchUpdateTarget(nodes, types, target)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			explicit[id] = true
		}
	}

	var out []*PlanNode
	for _, n := range nodes {
		switch {
		case explicit[n.ID]:
			// Deliberately named — runs even when locked.
			out = append(out, n)
		case bulk || bulkTypes[n.ResourceType]:
			if n.Locked && n.Action == ActionUpgrade {
				n.Action = ActionSkip
				n.SkipReason = "locked (lock: true) — update it explicitly with --update=" + n.ID
			}
			out = append(out, n)
		}
	}
	return out, nil
}

// matchUpdateTarget resolves a node-level target string to plan node IDs.
// Accepted forms, tried in order:
//
//	containers/frigate                exact node ID
//	flatpak/org.signal.Signal         <resourceType>/<DisplayName>
//	containers.frigate                <resourceType>.<name> (first dot splits)
//	frigate                           bare DisplayName (must be unambiguous)
func matchUpdateTarget(nodes []*PlanNode, types map[string]bool, target string) ([]string, error) {
	for _, n := range nodes {
		if n.ID == target || n.ResourceType+"/"+n.DisplayName == target {
			return []string{n.ID}, nil
		}
	}

	// Dotted form: only when the part before the first dot is a known
	// resource type — flatpak app IDs legitimately contain dots.
	if i := strings.Index(target, "."); i > 0 && !strings.Contains(target, "/") {
		if resType, name := target[:i], target[i+1:]; types[resType] && name != "" {
			for _, n := range nodes {
				if n.ResourceType == resType && (n.ID == resType+"/"+name || n.DisplayName == name) {
					return []string{n.ID}, nil
				}
			}
		}
	}

	// Bare display name across all resource types.
	var ids []string
	for _, n := range nodes {
		if n.DisplayName == target {
			ids = append(ids, n.ID)
		}
	}
	if len(ids) == 1 {
		return ids, nil
	}
	if len(ids) > 1 {
		return nil, fmt.Errorf("update target %q is ambiguous — matches %s", target, strings.Join(ids, ", "))
	}
	return nil, fmt.Errorf("no updatable resource matches %q (available: %s)", target, availableUpdateTargets(nodes))
}

// availableUpdateTargets renders a concise list of valid targets for error
// text. Internal node IDs (e.g. packages/__upgrade__) are omitted — they are
// implementation details reached via their resource type.
func availableUpdateTargets(nodes []*PlanNode) string {
	seen := make(map[string]bool)
	var out []string
	for _, n := range nodes {
		targets := []string{n.ResourceType}
		if !strings.Contains(n.ID, "__") {
			targets = append(targets, n.ID)
		}
		for _, t := range targets {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
