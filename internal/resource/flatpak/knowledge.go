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

	// List installed remotes. Scoped to --user: every command this resource
	// runs is user-scoped, and a system-level remote cannot serve a
	// "flatpak install --user" (it fails with "No remote refs found"), so a
	// system remote with the same name must not satisfy the plan.
	result, err := r.exec.Run(ctx, executor.Options{}, "flatpak", "remotes", "--user", "--columns=name")
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

	// List installed applications, also user-scoped: a system-wide install
	// can be neither updated nor removed with the --user commands this
	// resource issues, so it is not treated as satisfying the config.
	result, err = r.exec.Run(ctx, executor.Options{}, "flatpak", "list", "--app", "--user", "--columns=application")
	if err != nil {
		return nil, fmt.Errorf("listing flatpak apps: %w\nstderr: %s", err, result.Stderr)
	}
	for _, appID := range splitLines(result.Stdout) {
		entries = append(entries, engine.KnowledgeEntry{ID: appNodeID(appID)})
	}

	return entries, nil
}
