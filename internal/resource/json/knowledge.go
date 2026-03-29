package json

import (
	"context"
	"os"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
)

// GatherKnowledge reports all configured json entries whose target files exist
// on disk as present. Files that do not exist yet are omitted (triggering ADD).
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(_ context.Context) ([]engine.KnowledgeEntry, error) {
	var entries []engine.KnowledgeEntry
	for _, j := range r.cfg.Json {
		id := config.NodeID("json", j.File)
		if _, err := os.Stat(j.File); err == nil {
			entries = append(entries, engine.KnowledgeEntry{ID: id})
		}
	}
	return entries, nil
}
