package scripts

import (
	"context"

	"github.com/rayben/stay-go/internal/engine"
)

// GatherKnowledge reports all configured scripts as "present in knowledge".
// Scripts have no observable system state (unlike packages or users), so we
// treat every configured script as always present. This lets DetermineAction
// differentiate between "never run" (not in state → ADOPT, promoted to ADD)
// and "already run with same hash" (in state, hash matches → TRACK).
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(_ context.Context) ([]engine.KnowledgeEntry, error) {
	entries := make([]engine.KnowledgeEntry, 0, len(r.cfg.Scripts))
	for _, s := range r.cfg.Scripts {
		entries = append(entries, engine.KnowledgeEntry{ID: nodeID(s.Script)})
	}
	return entries, nil
}
