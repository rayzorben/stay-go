package commands

import (
	"context"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
)

// GatherKnowledge reports all configured commands as present on the system.
// Commands have no independently observable system state — unlike packages or
// services there is no query to check whether a command was previously run.
// By reporting all as present, DetermineAction can return TRACK (hash match)
// or UPDATE (hash changed) instead of always returning ADD.
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(_ context.Context) ([]engine.KnowledgeEntry, error) {
	entries := make([]engine.KnowledgeEntry, 0, len(r.cfg.Commands))
	for _, c := range r.cfg.Commands {
		entries = append(entries, engine.KnowledgeEntry{
			ID: config.NodeID("commands", c.Name),
		})
	}
	return entries, nil
}
