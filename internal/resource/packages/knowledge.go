package packages

import (
	"context"
	"fmt"
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
)

// GatherKnowledge runs the detected package manager's list command and returns
// a KnowledgeEntry for every currently-installed package.
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(ctx context.Context) ([]engine.KnowledgeEntry, error) {
	if err := r.ensureManager(ctx); err != nil {
		return nil, err
	}

	result, err := r.exec.Run(ctx, executor.Options{Env: r.manager.Env},
		r.manager.ListCmd[0], r.manager.ListCmd[1:]...)
	if err != nil {
		return nil, fmt.Errorf("listing installed packages (%s): %w\nstderr: %s",
			r.manager.Name, err, result.Stderr)
	}

	var names []string
	if r.manager.ParseOutput != nil {
		names = r.manager.ParseOutput(result.Stdout)
	} else {
		names = splitLines(result.Stdout)
	}

	entries := make([]engine.KnowledgeEntry, 0, len(names))
	for _, name := range names {
		if name != "" {
			entries = append(entries, engine.KnowledgeEntry{
				ID: config.NodeID("packages", name),
			})
		}
	}

	// Arch: pacman -Qq omits virtual/provides names. Treat configured package
	// names as present when `pacman -Q --quiet <name>` succeeds (same query
	// users run to verify an install).
	if usesPacmanQqList(r.manager) {
		seen := make(map[string]bool, len(entries))
		for _, e := range entries {
			seen[e.ID] = true
		}
		for _, p := range r.cfg.Packages {
			if p.Remove {
				continue
			}
			id := config.NodeID("packages", p.Name)
			if seen[id] {
				continue
			}
			_, err := r.exec.Run(ctx, executor.Options{Env: r.manager.Env},
				"pacman", "-Q", "--quiet", p.Name)
			if err != nil {
				continue
			}
			entries = append(entries, engine.KnowledgeEntry{ID: id})
			seen[id] = true
		}
	}

	return entries, nil
}

// splitLines splits s on newlines and returns trimmed, non-empty lines.
func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
