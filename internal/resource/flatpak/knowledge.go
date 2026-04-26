package flatpak

import (
	"context"
	"fmt"

	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
)

// GatherKnowledge returns KnowledgeEntries for all installed Flatpak remotes
// and apps. Returns nil (not an error) when the flatpak binary is not yet
// installed — the packages resource will install it before any flatpak node runs.
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(ctx context.Context) ([]engine.KnowledgeEntry, error) {
	if !executor.Exists("flatpak") {
		return nil, nil
	}

	var entries []engine.KnowledgeEntry

	// List installed remotes (user + system).
	result, err := r.exec.Run(ctx, executor.Options{}, "flatpak", "remotes", "--columns=name")
	if err != nil {
		if result.ExitCode == 127 {
			// flatpak exists in PATH (e.g. a distrobox host-exec shim) but is not
			// truly functional in this environment — treat as not installed.
			return nil, nil
		}
		return nil, fmt.Errorf("listing flatpak remotes: %w\nstderr: %s", err, result.Stderr)
	}
	for _, name := range splitLines(result.Stdout) {
		entries = append(entries, engine.KnowledgeEntry{ID: remoteNodeID(name)})
	}

	// List installed applications (user + system).
	result, err = r.exec.Run(ctx, executor.Options{}, "flatpak", "list", "--app", "--columns=application")
	if err != nil {
		return nil, fmt.Errorf("listing flatpak apps: %w\nstderr: %s", err, result.Stderr)
	}
	for _, appID := range splitLines(result.Stdout) {
		entries = append(entries, engine.KnowledgeEntry{ID: appNodeID(appID)})
	}

	return entries, nil
}
