package engine

import (
	"fmt"
	"strings"
)

// topoSort returns nodes in a dependency-safe execution order using
// Kahn's algorithm. Edges reflect "must execute before" relationships:
//   - For ADD/UPDATE nodes: dependencies execute before dependents.
//   - For REMOVE nodes: dependents execute before dependencies (reversed edges).
//
// SKIP nodes are included in the output after all executable nodes.
// Returns an error if the graph contains a cycle.
func topoSort(nodes []*PlanNode) ([]*PlanNode, error) {
	// Build two index maps.
	byID := make(map[string]*PlanNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}

	// Build adjacency: predecessor → successors (who runs after me).
	// For REMOVE nodes we reverse the edges so that dependents are removed first.
	successors := make(map[string][]string, len(nodes))
	inDegree := make(map[string]int, len(nodes))
	for _, n := range nodes {
		if _, ok := inDegree[n.ID]; !ok {
			inDegree[n.ID] = 0
		}
	}

	for _, n := range nodes {
		if n.Action == ActionRemove {
			// Reverse: dependents of n must run before n during removal.
			// n depends on deps → deps run AFTER n in removal order.
			for _, depID := range n.DependsOn {
				if _, ok := byID[depID]; !ok {
					continue // dep not in this plan (already removed or never tracked)
				}
				// Edge: n → depID  (n must run before its dep during removal)
				successors[n.ID] = append(successors[n.ID], depID)
				inDegree[depID]++
			}
		} else {
			// Forward: deps run before n.
			for _, depID := range n.DependsOn {
				if _, ok := byID[depID]; !ok {
					continue // dep outside this plan
				}
				// Edge: depID → n
				successors[depID] = append(successors[depID], n.ID)
				inDegree[n.ID]++
			}
		}
	}

	// Kahn's: start with all zero-in-degree nodes.
	var queue []string
	for _, n := range nodes {
		if inDegree[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}

	var sorted []*PlanNode
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		sorted = append(sorted, byID[id])
		for _, succID := range successors[id] {
			inDegree[succID]--
			if inDegree[succID] == 0 {
				queue = append(queue, succID)
			}
		}
	}

	if len(sorted) != len(nodes) {
		return nil, fmt.Errorf("dependency cycle detected in plan graph")
	}
	return sorted, nil
}

// markSkips sets Action=ActionSkip on any node whose declared dependencies are
// not "promised" (i.e. will exist after execution). A dep is promised if its
// action is TRACK, ADOPT, ADD, or UPDATE. A dep is not promised if its action
// is REMOVE, SKIP, or if it is not in the plan at all.
//
// This runs iteratively until no more changes occur, to handle transitive skips.
func markSkips(nodes []*PlanNode) {
	byID := make(map[string]*PlanNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}

	changed := true
	for changed {
		changed = false
		for _, n := range nodes {
			if n.Action == ActionSkip || n.Action == ActionRemove {
				continue
			}
			var unmetIDs []string
			for _, depID := range n.DependsOn {
				dep, ok := byID[depID]
				if !ok {
					unmetIDs = append(unmetIDs, depID)
					continue
				}
				if dep.Action == ActionSkip || dep.Action == ActionRemove {
					unmetIDs = append(unmetIDs, depID)
				}
			}
			if len(unmetIDs) > 0 {
				n.Action = ActionSkip
				n.SkipReason = formatUnmetDeps(unmetIDs)
				changed = true
			}
		}
	}
}

// formatUnmetDeps groups a list of unmet dependency IDs (e.g. "packages/zoxide")
// by resource type and returns a concise human-readable string, e.g.:
//
//	"unmet deps — packages [zoxide, fzf], services [sddm]"
func formatUnmetDeps(ids []string) string {
	var order []string
	groups := make(map[string][]string)
	for _, id := range ids {
		slash := strings.Index(id, "/")
		resType, item := id, id
		if slash >= 0 {
			resType, item = id[:slash], id[slash+1:]
		}
		if _, seen := groups[resType]; !seen {
			order = append(order, resType)
		}
		groups[resType] = append(groups[resType], item)
	}
	parts := make([]string, 0, len(order))
	for _, resType := range order {
		parts = append(parts, resType+" ["+strings.Join(groups[resType], ", ")+"]")
	}
	return "unmet deps — " + strings.Join(parts, ", ")
}
