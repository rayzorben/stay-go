package engine

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
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
	// AutoYes skips the interactive confirmation prompt and proceeds automatically.
	// Intended for embedded use (e.g. stay-go running inside a distrobox).
	AutoYes bool
	// QuietPlan suppresses the plan table, summary, and leading blank line before
	// execution. Execution results are still streamed normally. Used when stay-go
	// runs embedded inside a distrobox — the parent already showed the plan.
	QuietPlan bool
	// ShowSkipped determines whether skipped packages are printed in the plan.
	ShowSkipped bool
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
		if !e.opts.QuietPlan {
			fmt.Fprintf(os.Stdout, "\n  Nothing to do — no resources configured.\n\n")
		}
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
			if !e.opts.QuietPlan {
				fmt.Fprintf(os.Stdout, "\n  %s✓%s  System up to date  %s·  %d managed%s\n\n",
					ansiGreen, ansiReset, ansiDim, trackCount, ansiReset)
			}
		} else {
			// ADOPT/SKIP nodes to display but no confirmation needed.
			if !e.opts.QuietPlan {
				DisplayPlan(os.Stdout, sorted, e.opts.ShowSkipped)
			}
		}
		if e.opts.DryRun {
			fmt.Fprintf(os.Stdout, "  %s(dry-run — no changes applied)%s\n\n", ansiDim, ansiReset)
			return nil
		}
	} else {
		if !e.opts.QuietPlan {
			DisplayPlan(os.Stdout, sorted, e.opts.ShowSkipped)
		}

		if e.opts.DryRun {
			fmt.Fprintf(os.Stdout, "  %s(dry-run — no changes applied)%s\n\n", ansiDim, ansiReset)
			return nil
		}

		if !e.opts.AutoYes {
			showSkippedLocal := e.opts.ShowSkipped
			for {
				hasHiddenSkipped := false
				if !showSkippedLocal {
					for _, n := range sorted {
						if !n.Hidden && n.Action == ActionSkip {
							hasHiddenSkipped = true
							break
						}
					}
				}

				resp, err := Confirm(os.Stdout, os.Stdin, hasHiddenSkipped)
				if err != nil {
					return fmt.Errorf("reading confirmation: %w", err)
				}
				
				if resp == ConfirmShowSkipped {
					showSkippedLocal = true
					fmt.Fprintln(os.Stdout)
					if !e.opts.QuietPlan {
						DisplayPlan(os.Stdout, sorted, showSkippedLocal)
					}
					continue
				} else if resp == ConfirmAbort {
					fmt.Fprintf(os.Stdout, "\n  Aborted.\n\n")
					return nil
				}
				
				break
			}
		}
	}

	// ── Step 6: Execute ───────────────────────────────────────────────────────
	executeErr := e.execute(ctx, sorted, st)

	// ── Step 7: Save state ────────────────────────────────────────────────────
	// Always save — per-node state updates from successful nodes must be
	// persisted even when other nodes failed.
	if err := state.Save(st, e.opts.StatePath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save state: %v\n", err)
	}
	return executeErr
}

// preSudo runs "sudo -v" once to cache credentials if any active node in the
// plan requires sudo. Prints a newline first so the password prompt appears on
// a clean line. Returns true if sudo is needed (so the caller can start a keepalive).
func (e *Engine) preSudo(ctx context.Context, nodes []*PlanNode) bool {
	for _, n := range nodes {
		if n.NeedsSudo && (n.Action == ActionAdd || n.Action == ActionUpdate || n.Action == ActionRemove) {
			fmt.Fprintln(os.Stdout)
			e.exec.Run(ctx, executor.Options{}, "sudo", "-v") //nolint:errcheck
			return true
		}
	}
	return false
}

// startSudoKeepalive runs "sudo -v -n" every 3 minutes in the background so
// that long-running operations do not let the credential cache expire mid-run.
// The returned stop function must be called when execution finishes.
func (e *Engine) startSudoKeepalive(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(3 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// -n: non-interactive — refreshes only if already authenticated.
				e.exec.Run(ctx, executor.Options{}, "sudo", "-v", "-n") //nolint:errcheck
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() { close(done) }
}

// execute processes nodes in topological order with runtime failure propagation.
// REMOVE nodes are executed first (in their pre-sorted reverse-dep order),
// then ADD/UPDATE nodes (forward dep order), then TRACK nodes (state update only).
func (e *Engine) execute(ctx context.Context, nodes []*PlanNode, st *state.State) error {
	if e.preSudo(ctx, nodes) {
		stopKeepalive := e.startSudoKeepalive(ctx)
		defer stopKeepalive()
	}

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
	var hasFailed bool
	for _, node := range nodes {
		switch node.Action {
		case ActionTrack, ActionAdopt, ActionLevel:
			// No system commands needed; update state to sync hash, level, and data.
			st.Set(node.ID, node.ConfigHash, node.Level, node.StateData)
			// Call Execute so resources can perform per-node work on TRACK/ADOPT
			// (e.g. secrets resource decrypts values into cfg.DecryptedSecrets).
			r := resourceByType[node.ResourceType]
			if execErr := r.Execute(ctx, node, st); execErr != nil {
				DisplayExecutionResult(os.Stdout, node, execErr, 0)
				succeeded[node.ID] = false
				hasFailed = true
			}

		case ActionAdd, ActionUpdate, ActionRemove:
			// Runtime dep check: all deps that are going to execute must have succeeded.
			if !e.depsSucceeded(node, succeeded) {
				node.Action = ActionSkip
				node.SkipReason = "dependency failed during execution"
				if !node.Hidden {
					DisplayExecutionResult(os.Stdout, node, nil, 0)
				}
				succeeded[node.ID] = false
				continue
			}
			// REMOVE for a node confirmed absent from the system: skip the system
			// command entirely and just clean up state. Avoids "not found" failures
			// when a resource was removed from both config and the live system.
			if node.Action == ActionRemove && node.AbsentFromSystem {
				st.Delete(node.ID)
				succeeded[node.ID] = true
				if !node.Hidden {
					DisplayExecutionResult(os.Stdout, node, nil, 0)
				}
				continue
			}
			if !node.Hidden {
				DisplayExecutionProgress(os.Stdout, node, "")
			}
			if node.NeedsSudo && !node.Hidden {
				// Commit the progress line — sudo will prompt via /dev/tty and
				// we need a clean line for the password prompt to appear on.
				fmt.Fprintln(os.Stdout)
			} else if !node.NeedsSudo && !node.Hidden {
				e.exec.ProgressFn = func(line string) {
					DisplayExecutionProgress(os.Stdout, node, line)
				}
			}
			start := time.Now()
			r := resourceByType[node.ResourceType]
			execErr := r.Execute(ctx, node, st)
			e.exec.ProgressFn = nil
			dur := time.Since(start)
			node.ExecutionErr = execErr
			succeeded[node.ID] = execErr == nil
			if execErr != nil {
				hasFailed = true
			}
			if !node.Hidden {
				DisplayExecutionResult(os.Stdout, node, execErr, dur)
			}

		case ActionSkip:
			// In quiet-plan mode (inside distrobox), the plan was suppressed, so
			// show skipped nodes with their reason during the execution phase.
			if e.opts.QuietPlan {
				DisplayExecutionResult(os.Stdout, node, nil, 0)
			}
			succeeded[node.ID] = false
		}
	}

	if !e.opts.QuietPlan {
		e.printFinalReport(nodes)
	}
	if hasFailed {
		return ErrExecutionFailed
	}
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

// printFinalReport prints the post-execution summary with a divider.
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

	tw := termWidth()
	fmt.Fprintln(os.Stdout)
	divider(os.Stdout, tw)
	fmt.Fprintln(os.Stdout)

	if failed > 0 {
		fmt.Fprintf(os.Stdout, "  %s✗ %d failed%s  %s·%s  %s✓ %d applied%s  %s·%s  %s! %d skipped%s\n",
			ansiRed, failed, ansiReset,
			ansiDim, ansiReset,
			ansiGreen, success, ansiReset,
			ansiDim, ansiReset,
			ansiDim, skipped, ansiReset,
		)
	} else {
		fmt.Fprintf(os.Stdout, "  %s✓ %d applied%s  %s·%s  %s! %d skipped%s\n",
			ansiGreen, success, ansiReset,
			ansiDim, ansiReset,
			ansiDim, skipped, ansiReset,
		)
	}
	fmt.Fprintln(os.Stdout)
}
