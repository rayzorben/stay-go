package distrobox

import (
	"context"
	"fmt"
	"strings"

	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/executor"
)

// GatherKnowledge reports which configured distrobox containers currently exist
// on the host. A box is "known" when it appears in `distrobox list` output
// regardless of whether it is running or stopped.
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(ctx context.Context) ([]engine.KnowledgeEntry, error) {
	if len(r.cfg.Distrobox) == 0 {
		return nil, nil
	}

	existing, err := listDistroboxes(ctx, r.exec)
	if err != nil {
		// distrobox not installed or unavailable — not fatal; plan will ADD all boxes.
		return nil, nil
	}

	var entries []engine.KnowledgeEntry
	for _, entry := range r.cfg.Distrobox {
		if existing[entry.Name] {
			entries = append(entries, engine.KnowledgeEntry{ID: boxNodeID(entry.Name)})
		}
	}
	return entries, nil
}

// listDistroboxes returns the set of distrobox names currently registered on
// the host. Parses the NAME column from `distrobox list --no-color` output.
// The header line and any blank lines are skipped.
//
// Example output:
//
//	ID           | NAME      | STATUS    | IMAGE
//	1            | sedanos   | Up 2 hours| docker.io/cachyos/cachyos-v3:latest
func listDistroboxes(ctx context.Context, exec *executor.Executor) (map[string]bool, error) {
	result, err := exec.Run(ctx, executor.Options{}, "distrobox", "list", "--no-color")
	if err != nil || result.ExitCode != 0 {
		return nil, fmt.Errorf("distrobox list: exit %d", result.ExitCode)
	}

	names := make(map[string]bool)
	for i, line := range strings.Split(result.Stdout, "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue // skip header and blank lines
		}
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSpace(parts[1])
		if name != "" && name != "NAME" {
			names[name] = true
		}
	}
	return names, nil
}
