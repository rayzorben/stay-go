package engine

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/rayben/stay-go/internal/executor"
	"github.com/rayben/stay-go/internal/state"
)

// Options configures the engine's behaviour.
type Options struct {
	// ConfigPath is the path to the YAML desired-state file.
	ConfigPath string
	// StatePath is the path to the persisted state JSON file.
	StatePath string
	// Debug enables verbose command output streaming.
	Debug bool
	// DryRun prints the plan but does not execute or save state.
	DryRun bool
}

// Engine orchestrates knowledge gathering, plan building, and execution
// across all registered resource types.
type Engine struct {
	opts      Options
	resources []Resource
	exec      *executor.Executor
}

// New creates an Engine with the given options and a shared executor used
// for pre-authenticating sudo before the execute loop.
func New(opts Options, exec *executor.Executor) *Engine {
	return &Engine{opts: opts, exec: exec}
}

// Register appends a Resource implementation. Resources are displayed and
// executed in the order they are registered.
func (e *Engine) Register(r Resource) {
	e.resources = append(e.resources, r)
}

// Run executes the full apply pipeline:
//  1. GatherKnowledge (all resources in parallel)
//  2. BuildPlan (sequential, in registration order)
//  3. MarkSkips (dependency resolution)
//  4. TopologicalSort
//  5. DisplayPlan + Confirm
//  6. Execute (topo order, with failure propagation)
//  7. SaveState
func (e *Engine) Run(ctx context.Context, st *state.State) error {
	// ── Step 1: Gather knowledge in parallel ──────────────────────────────────
	knowledgeResults := make([][]KnowledgeEntry, len(e.resources))
	knowledgeErrors := make([]error, len(e.resources))
	var wg sync.WaitGroup
	for i, r := range e.resources {
		wg.Add(1)
		go func(i int, r Resource) {
			defer wg.Done()
			knowledgeResults[i], knowledgeErrors[i] = r.GatherKnowledge(ctx)
		}(i, r)
	}
	wg.Wait()

	// Collect all errors.
	for i, err := range knowledgeErrors {
		if err != nil {
			return fmt.Errorf("gathering knowledge for %q: %w", e.resources[i].Type(), err)
		}
	}

	// Build a unified knowledge map: nodeID → present.
	knowledge := make(map[string]bool)
	for _, entries := range knowledgeResults {
		for _, entry := range entries {
			knowledge[entry.ID] = true
		}
	}

	// ── Step 2: Build plan ────────────────────────────────────────────────────
	var allNodes []*PlanNode
	for _, r := range e.resources {
		nodes, err := r.BuildPlan(ctx, knowledge, st)
		if err != nil {
			return fmt.Errorf("building plan for %q: %w", r.Type(), err)
		}
		allNodes = append(allNodes, nodes...)
	}

	if len(allNodes) == 0 {
		fmt.Fprintln(os.Stdout, "Nothing to do — system matches config.")
		return nil
	}

	// ── Step 3: Mark skips (dependency resolution) ───────────────────────────
	markSkips(allNodes)

	// ── Step 4: Topological sort ──────────────────────────────────────────────
	sorted, err := topoSort(allNodes)
	if err != nil {
		return fmt.Errorf("sorting plan: %w", err)
	}

	// ── Step 5: Display plan and confirm ─────────────────────────────────────
	// Check whether any node requires actual system changes.
	hasChanges := false
	for _, n := range sorted {
		if n.Action == ActionAdd || n.Action == ActionUpdate || n.Action == ActionRemove {
			hasChanges = true
			break
		}
	}

	// Count visible non-TRACK nodes for display decisions.
	// ActionLevel is visible but requires no confirmation.
	visibleCount := 0
	for _, n := range sorted {
		if n.Action != ActionTrack {
			visibleCount++
		}
	}

	trackCount := 0
	for _, n := range sorted {
		if n.Action == ActionTrack {
			trackCount++
		}
	}

	if !hasChanges {
		if visibleCount == 0 {
			// Everything is already managed — fully silent.
			fmt.Fprintf(os.Stdout, "%sSystem is up to date.%s (%d managed)\n", ansiGreen, ansiReset, trackCount)
		} else {
			// ADOPT/SKIP nodes to display but no confirmation needed.
			DisplayPlan(os.Stdout, sorted)
			DisplaySummary(os.Stdout, sorted)
		}
		if e.opts.DryRun {
			fmt.Fprintln(os.Stdout, "(dry-run: no changes made)")
			return nil
		}
	} else {
		DisplayPlan(os.Stdout, sorted)
		DisplaySummary(os.Stdout, sorted)

		if e.opts.DryRun {
			fmt.Fprintln(os.Stdout, "(dry-run: no changes made)")
			return nil
		}

		ok, err := Confirm(os.Stdout, os.Stdin)
		if err != nil {
			return fmt.Errorf("reading confirmation: %w", err)
		}
		if !ok {
			fmt.Fprintln(os.Stdout, "Aborted.")
			return nil
		}
	}

	// ── Step 6: Execute ───────────────────────────────────────────────────────
	fmt.Fprintln(os.Stdout)
	if err := e.execute(ctx, sorted, st); err != nil {
		return err
	}

	// ── Step 7: Save state ────────────────────────────────────────────────────
	if err := state.Save(st, e.opts.StatePath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save state: %v\n", err)
	}
	return nil
}

// preSudo runs "sudo -v" once to cache credentials if any active node in the
// plan requires sudo. This ensures a single password prompt at the start of
// execution rather than interrupting individual operations mid-plan.
func (e *Engine) preSudo(ctx context.Context, nodes []*PlanNode) {
	for _, n := range nodes {
		if n.NeedsSudo && (n.Action == ActionAdd || n.Action == ActionUpdate || n.Action == ActionRemove) {
			e.exec.Run(ctx, executor.Options{}, "sudo", "-v") //nolint:errcheck
			return
		}
	}
}

// execute processes nodes in topological order with runtime failure propagation.
// REMOVE nodes are executed first (in their pre-sorted reverse-dep order),
// then ADD/UPDATE nodes (forward dep order), then TRACK nodes (state update only).
func (e *Engine) execute(ctx context.Context, nodes []*PlanNode, st *state.State) error {
	e.preSudo(ctx, nodes)

	resourceByType := make(map[string]Resource, len(e.resources))
	for _, r := range e.resources {
		resourceByType[r.Type()] = r
	}

	// Runtime success set: nodeID → true if the node was successfully resolved.
	// Pre-populate TRACK, ADOPT, and LEVEL nodes since they are already on the system.
	succeeded := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.Action == ActionTrack || n.Action == ActionAdopt || n.Action == ActionLevel {
			succeeded[n.ID] = true
		}
	}

	// Process nodes in topo order. The graph ensures REMOVE dependents before
	// deps, and ADD/UPDATE deps before dependents.
	for _, node := range nodes {
		switch node.Action {
		case ActionTrack, ActionAdopt, ActionLevel:
			// No system commands needed; update state to sync hash, level, and data.
			st.Set(node.ID, node.ConfigHash, node.Level, node.StateData)
			// Call Execute so resources can perform per-node work on TRACK/ADOPT
			// (e.g. secrets resource decrypts values into cfg.DecryptedSecrets).
			r := resourceByType[node.ResourceType]
			if execErr := r.Execute(ctx, node, st); execErr != nil {
				DisplayExecutionResult(os.Stdout, node, execErr)
				succeeded[node.ID] = false
			}

		case ActionAdd, ActionUpdate, ActionRemove:
			// Runtime dep check: all deps that are going to execute must have succeeded.
			if !e.depsSucceeded(node, succeeded) {
				node.Action = ActionSkip
				node.SkipReason = "dependency failed during execution"
				DisplayExecutionResult(os.Stdout, node, nil)
				succeeded[node.ID] = false
				continue
			}
			DisplayExecutionProgress(os.Stdout, node)
			r := resourceByType[node.ResourceType]
			execErr := r.Execute(ctx, node, st)
			node.ExecutionErr = execErr
			succeeded[node.ID] = execErr == nil
			DisplayExecutionResult(os.Stdout, node, execErr)

		case ActionSkip:
			succeeded[node.ID] = false
		}
	}

	fmt.Fprintln(os.Stdout)
	e.printFinalReport(nodes)
	return nil
}

// depsSucceeded checks whether all of node's dependencies are in the succeeded set.
// Only nodes that are part of this plan are checked; external deps are ignored
// (they were handled by markSkips at plan time).
func (e *Engine) depsSucceeded(node *PlanNode, succeeded map[string]bool) bool {
	for _, depID := range node.DependsOn {
		if ok, exists := succeeded[depID]; exists && !ok {
			return false
		}
	}
	return true
}

// printFinalReport prints a summary of execution results.
func (e *Engine) printFinalReport(nodes []*PlanNode) {
	var success, failed, skipped int
	for _, n := range nodes {
		switch {
		case n.ExecutionErr != nil:
			failed++
		case n.Action == ActionSkip:
			skipped++
		case n.Action == ActionTrack, n.Action == ActionAdopt, n.Action == ActionLevel:
			// Not counted as active changes.
		default:
			success++
		}
	}
	if failed > 0 {
		fmt.Fprintf(os.Stdout, "%s%d succeeded, %d failed, %d skipped%s\n",
			ansiYellow, success, failed, skipped, ansiReset)
	} else {
		fmt.Fprintf(os.Stdout, "%sAll done. %d applied, %d skipped.%s\n",
			ansiGreen, success, skipped, ansiReset)
	}
}
