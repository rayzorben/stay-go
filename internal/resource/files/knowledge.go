package files

import (
	"context"
	"os"

	"github.com/rayben/stay-go/internal/engine"
)

// GatherKnowledge checks whether each configured target exists on disk.
// Uses Lstat so symlinks count as present.
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(_ context.Context) ([]engine.KnowledgeEntry, error) {
	var entries []engine.KnowledgeEntry
	for i := range r.cfg.Files {
		target := r.cfg.Files[i].Target
		if _, err := os.Lstat(target); err == nil {
			entries = append(entries, engine.KnowledgeEntry{ID: nodeID(target)})
		}
	}
	return entries, nil
}
