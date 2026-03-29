package packages

import (
	"context"
	"fmt"
	"strings"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/executor"
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
