package files

import (
	"context"
	"os"

	"github.com/rayzorben/stay-go/internal/engine"
)

// GatherKnowledge checks whether each configured target satisfies desired state.
// Default inline files: target exists (Lstat; symlinks count as present).
// Additive inline content (content + add: true): target contains the snippet.
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(_ context.Context) ([]engine.KnowledgeEntry, error) {
	var entries []engine.KnowledgeEntry
	for i := range r.cfg.Files {
		entry := &r.cfg.Files[i]
		target := entry.Target
		if entry.Content != "" && entry.Add {
			if fileContainsSnippet(target, entry.Content) {
				entries = append(entries, engine.KnowledgeEntry{ID: nodeID(target)})
			}
			continue
		}
		if _, err := os.Lstat(target); err == nil {
			entries = append(entries, engine.KnowledgeEntry{ID: nodeID(target)})
		}
	}
	return entries, nil
}
