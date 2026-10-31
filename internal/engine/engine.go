package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/runlog"
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
	// ApplyTargets restricts the plan to the selected node IDs and their deps.
	ApplyTargets []string
	// Sync marks plan nodes as done in state without executing any commands.
	// Use this when you have manually applied a change and want stay-go to stop
	// re-attempting it.
	Sync bool
	// Log records the plan, confirmation, and each node's execution result in
	// the run log. Nil disables it.
	Log *runlog.Log
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
	e.opts.Log.Printf("gathering knowledge")
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
	e.opts.Log.Printf("building plan")
	var allNodes []*PlanNode
	for _, r := range e.resources {
		nodes, err := r.BuildPlan(ctx, knowledge, st)
		if err != nil {
			return fmt.Errorf("building plan for %q: %w", r.Type(), err)
		}
		allNodes = append(allNodes, nodes...)
	}

	if len(allNodes) == 0 {
		e.opts.Log.Printf("nothing to do — no resources configured")
		if !e.opts.QuietPlan {
			fmt.Fprintf(os.Stdout, "\n  Nothing to do — no resources configured.\n\n")
		}
		return nil
	}

	// Every other resource depends on the secrets being resolved into the config
	// first (the secrets resource decrypts and substitutes plaintext during its
	// Execute). Wired here, once — resources never declare secret dependencies.
	addSecretBarrierDeps(allNodes)

	if len(e.opts.ApplyTargets) > 0 {
		filtered, err := filterPlanNodes(allNodes, e.opts.ApplyTargets)
		if err != nil {
			return err
		}
		if len(filtered) == 0 {
			return fmt.Errorf("no matching plan nodes for --apply")
		}
		allNodes = filtered
	}

	// ── Step 3: Mark skips (dependency resolution) ───────────────────────────
	markSkips(allNodes)

	// ── Step 4: Topological sort ──────────────────────────────────────────────
	sorted, err := topoSort(allNodes)
	if err != nil {
		return fmt.Errorf("sorting plan: %w", err)
	}
	logPlan(e.opts.Log, sorted)

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
			e.opts.Log.Printf("dry-run — no changes applied")
			fmt.Fprintf(os.Stdout, "  %s(dry-run — no changes applied)%s\n\n", ansiDim, ansiReset)
			return nil
		}
	} else {
		if !e.opts.QuietPlan {
			DisplayPlan(os.Stdout, sorted, e.opts.ShowSkipped)
		}

		if e.opts.DryRun {
			e.opts.Log.Printf("dry-run — no changes applied")
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
					e.opts.Log.Printf("aborted at confirmation prompt")
					fmt.Fprintf(os.Stdout, "\n  Aborted.\n\n")
					return nil
				}
				
				break
			}
		}
	}

	// ── Step 6: Execute (or sync) ────────────────────────────────────────────
	if e.opts.Sync {
		fmt.Fprintf(os.Stdout, "  %s(sync — state updated, no commands executed)%s\n\n", ansiDim, ansiReset)
	}

	var executeErr error
	if e.opts.Sync {
		executeErr = e.syncState(sorted, st)
	} else {
		executeErr = e.execute(ctx, sorted, st)
	}

	// ── Step 7: Save state ────────────────────────────────────────────────────
	// Always save — per-node state updates from successful nodes must be
	// persisted even when other nodes failed.
	if err := state.Save(st, e.opts.StatePath); err != nil {
		e.opts.Log.Printf("ERROR saving state: %v", err)
		fmt.Fprintf(os.Stderr, "warning: failed to save state: %v\n", err)
	}
	return executeErr
}

func filterPlanNodes(nodes []*PlanNode, targets []string) ([]*PlanNode, error) {
	if len(targets) == 0 {
		return nodes, nil
	}

	byID := make(map[string]*PlanNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}

	allowed := make(map[string]bool)
	var visit func(id string)
	visit = func(id string) {
		if allowed[id] {
			return
		}
		node, ok := byID[id]
		if !ok {
			return
		}
		allowed[id] = true
		for _, dep := range node.DependsOn {
			visit(dep)
		}
	}

	for _, target := range targets {
		normalized, err := normalizeApplyTarget(target)
		if err != nil {
			return nil, err
		}
		if _, ok := byID[normalized]; !ok {
			return nil, fmt.Errorf("no matching plan node for --apply target %q", target)
		}
		visit(normalized)
	}

	var filtered []*PlanNode
	for _, n := range nodes {
		if allowed[n.ID] {
			filtered = append(filtered, n)
		}
	}
	return filtered, nil
}

func normalizeApplyTarget(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("empty --apply target")
	}
	if strings.Contains(target, "=") {
		parts := strings.SplitN(target, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
			return "", fmt.Errorf("invalid --apply target %q", target)
		}
		resource := strings.TrimSpace(parts[0])
		if resource == "" {
			return "", fmt.Errorf("invalid --apply target %q", target)
		}
		return resourceNamePair(resource, strings.TrimSpace(parts[1])), nil
	}
	if strings.Contains(target, "/") {
		return target, nil
	}
	if strings.Contains(target, ".") {
		parts := strings.Split(target, ".")
		if len(parts) < 2 {
			return "", fmt.Errorf("invalid --apply target %q", target)
		}
		return parts[0] + "/" + parts[len(parts)-1], nil
	}
	return "", fmt.Errorf("invalid --apply target %q", target)
}

func resourceNamePair(resource, name string) string {
	parts := strings.Split(resource, ".")
	return parts[0] + "/" + name
}

// preSudo runs "sudo -v" once to cache credentials if any active node in the
// plan requires sudo. Prints a newline first so the password prompt appears on
// a clean line. Returns true if sudo is needed (so the caller can start a keepalive).
func (e *Engine) preSudo(ctx context.Context, nodes []*PlanNode) bool {
	for _, n := range nodes {
		if n.NeedsSudo && (n.Action == ActionAdd || n.Action == ActionUpdate || n.Action == ActionRemove || n.Action == ActionUpgrade) {
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
	e.opts.Log.Printf("executing")
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
	// deps, and ADD/UPDATE deps before dependents. processed records every node
	// already handled — including nodes pulled forward into an earlier batch —
	// and preds lists what each node must run after (used to decide batching).
	processed := make(map[string]bool, len(nodes))
	preds := predecessors(nodes)
	var hasFailed bool
	for i, node := range nodes {
		if processed[node.ID] {
			continue // already executed as part of a batch
		}
		processed[node.ID] = true
		switch node.Action {
		case ActionTrack, ActionAdopt, ActionLevel:
			// No system commands needed; update state to sync sourceFile and data.
			st.Set(node.ID, node.ConfigHash, node.SourceFile, node.StateData)
			// Call Execute so resources can perform per-node work on TRACK/ADOPT
			// (e.g. secrets resource decrypts values into cfg.DecryptedSecrets).
			r := resourceByType[node.ResourceType]
			if execErr := r.Execute(ctx, node, st); execErr != nil {
				node.ExecutionErr = execErr
				logResult(e.opts.Log, node, 0)
				DisplayExecutionResult(os.Stdout, node, execErr, 0)
				succeeded[node.ID] = false
				hasFailed = true
			}

		case ActionAdd, ActionUpdate, ActionRemove, ActionUpgrade:
			// Runtime dep check: all deps that are going to execute must have succeeded.
			if !e.depsSucceeded(node, succeeded) {
				node.Action = ActionSkip
				node.SkipReason = "dependency failed during execution"
				logResult(e.opts.Log, node, 0)
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
				e.opts.Log.Printf("%s %s: absent from system, state entry removed", node.Action, node.ID)
				if !node.Hidden {
					DisplayExecutionResult(os.Stdout, node, nil, 0)
				}
				continue
			}
			if batch := e.collectBatch(nodes, i, resourceByType, preds, processed, succeeded); len(batch) > 1 {
				if e.executeBatch(ctx, batch, resourceByType[node.ResourceType].(BatchExecutor), st, succeeded) {
					hasFailed = true
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
			e.opts.Log.Printf("%s %s: start", node.Action, node.ID)
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
			logResult(e.opts.Log, node, dur)
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

	logSummary(e.opts.Log, nodes)
	if !e.opts.QuietPlan {
		e.printFinalReport(nodes)
	}
	if hasFailed {
		return ErrExecutionFailed
	}
	return nil
}

// collectBatch returns the batch led by nodes[i] when its resource implements
// BatchExecutor: nodes[i] plus every later, not-yet-processed node of the same
// resource type with the same non-empty BatchKey that is ready to run now —
// all its predecessors already processed and its deps succeeded. Pulling such
// a node forward is safe: topo order only requires its predecessors to run
// first, and nothing between it and the leader can depend on it. Marks every
// returned node processed. Returns nil when the leader is not batchable.
func (e *Engine) collectBatch(nodes []*PlanNode, i int, resourceByType map[string]Resource,
	preds map[string][]string, processed, succeeded map[string]bool) []*PlanNode {
	leader := nodes[i]
	be, ok := resourceByType[leader.ResourceType].(BatchExecutor)
	if !ok || leader.Hidden {
		return nil
	}
	key := be.BatchKey(leader)
	if key == "" {
		return nil
	}
	batch := []*PlanNode{leader}
	for _, n := range nodes[i+1:] {
		if processed[n.ID] || n.Hidden || n.ResourceType != leader.ResourceType {
			continue
		}
		switch n.Action {
		case ActionAdd, ActionUpdate, ActionRemove, ActionUpgrade:
		default:
			continue
		}
		if n.Action == ActionRemove && n.AbsentFromSystem {
			continue // state-only cleanup — handled by the main loop
		}
		if be.BatchKey(n) != key || !e.depsSucceeded(n, succeeded) {
			continue
		}
		ready := true
		for _, p := range preds[n.ID] {
			if !processed[p] || !succeeded[p] {
				ready = false
				break
			}
		}
		if ready {
			batch = append(batch, n)
		}
	}
	for _, n := range batch[1:] {
		processed[n.ID] = true
	}
	return batch
}

// executeBatch runs batch through be.ExecuteBatch with a single progress line,
// then reports, logs and records the outcome of every node. Returns true if
// any node failed.
func (e *Engine) executeBatch(ctx context.Context, batch []*PlanNode, be BatchExecutor,
	st *state.State, succeeded map[string]bool) bool {
	leader := batch[0]
	ids := make([]string, len(batch))
	for j, n := range batch {
		ids[j] = n.ID
	}
	e.opts.Log.Printf("%s batch of %d: start [%s]", leader.Action, len(batch), strings.Join(ids, " "))

	// One progress row stands in for the whole batch while it runs.
	progress := &PlanNode{
		ResourceType: leader.ResourceType,
		DisplayName:  fmt.Sprintf("%d %s", len(batch), leader.ResourceType),
		Action:       leader.Action,
	}
	DisplayExecutionProgress(os.Stdout, progress, "")
	needsSudo := false
	for _, n := range batch {
		needsSudo = needsSudo || n.NeedsSudo
	}
	if needsSudo {
		fmt.Fprintln(os.Stdout)
	} else {
		e.exec.ProgressFn = func(line string) {
			DisplayExecutionProgress(os.Stdout, progress, line)
		}
	}

	start := time.Now()
	errs := be.ExecuteBatch(ctx, batch, st)
	e.exec.ProgressFn = nil
	dur := time.Since(start)

	failed := false
	for j, n := range batch {
		var err error
		if j < len(errs) {
			err = errs[j]
		}
		n.ExecutionErr = err
		succeeded[n.ID] = err == nil
		failed = failed || err != nil
		logResult(e.opts.Log, n, dur)
		DisplayExecutionResult(os.Stdout, n, err, dur)
	}
	return failed
}

// syncState updates persisted state to match the plan without executing any
// system commands. ADD/UPDATE nodes are written to state as if they succeeded;
// REMOVE nodes are deleted from state; SKIP nodes are left untouched.
func (e *Engine) syncState(nodes []*PlanNode, st *state.State) error {
	e.opts.Log.Printf("sync — marking plan done in state, no commands executed")
	for _, node := range nodes {
		switch node.Action {
		case ActionTrack, ActionAdopt, ActionLevel:
			st.Set(node.ID, node.ConfigHash, node.SourceFile, node.StateData)
		case ActionAdd, ActionUpdate:
			st.Set(node.ID, node.ConfigHash, node.SourceFile, node.StateData)
			DisplayExecutionResult(os.Stdout, node, nil, 0)
		case ActionRemove:
			st.Delete(node.ID)
			DisplayExecutionResult(os.Stdout, node, nil, 0)
		}
	}
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

// logPlan records the sorted plan in the run log: one line per node that is
// not a plain TRACK (those are counted), with its reason, declared deps, and
// notes.
func logPlan(lg *runlog.Log, nodes []*PlanNode) {
	if lg == nil {
		return
	}
	tracked := 0
	for _, n := range nodes {
		if n.Action == ActionTrack {
			tracked++
			continue
		}
		line := fmt.Sprintf("plan: %-7s %s", n.Action, n.ID)
		if n.Hidden {
			line += " (hidden)"
		}
		if n.Description != "" {
			line += " — " + n.Description
		}
		if n.SkipReason != "" {
			line += " — skip: " + n.SkipReason
		}
		// The secrets barrier makes every node depend on every secret; listing
		// those on each line would bury the deps the config actually declares.
		var deps []string
		for _, d := range n.DependsOn {
			if n.ResourceType == "secrets" || !strings.HasPrefix(d, "secrets/") {
				deps = append(deps, d)
			}
		}
		if len(deps) > 0 {
			line += " [deps: " + strings.Join(deps, ", ") + "]"
		}
		lg.Printf("%s", line)
		for _, note := range n.Notes {
			lg.Printf("plan:           ↳ %s", note)
		}
	}
	lg.Printf("plan: %d node(s), %d tracked unchanged", len(nodes), tracked)
}

// logResult records one node's execution outcome in the run log.
func logResult(lg *runlog.Log, node *PlanNode, dur time.Duration) {
	switch {
	case node.ExecutionErr != nil:
		lg.Printf("%s %s: FAILED after %s: %v", node.Action, node.ID, dur.Round(time.Millisecond), node.ExecutionErr)
	case node.Action == ActionSkip:
		lg.Printf("SKIP %s: %s", node.ID, node.SkipReason)
	default:
		lg.Printf("%s %s: ok in %s", node.Action, node.ID, dur.Round(time.Millisecond))
	}
}

// logSummary records the execution totals and lists every failed node, so the
// failures can be found without reading the whole log.
func logSummary(lg *runlog.Log, nodes []*PlanNode) {
	if lg == nil {
		return
	}
	var applied, skipped int
	var failed []string
	for _, n := range nodes {
		switch {
		case n.ExecutionErr != nil:
			failed = append(failed, n.ID)
		case n.Action == ActionSkip:
			skipped++
		case n.Action == ActionTrack, n.Action == ActionAdopt, n.Action == ActionLevel:
		default:
			applied++
		}
	}
	lg.Printf("summary: %d failed, %d applied, %d skipped", len(failed), applied, skipped)
	for _, id := range failed {
		lg.Printf("summary: FAILED %s", id)
	}
}
