package groups

import (
	"context"
	"fmt"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
)

// GatherKnowledge reads /etc/group and returns a KnowledgeEntry for every
// system group. Using the native file avoids spawning processes and is
// consistent with how the users resource reads /etc/passwd.
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(_ context.Context) ([]engine.KnowledgeEntry, error) {
	sysGroups, err := parseGroup()
	if err != nil {
		return nil, fmt.Errorf("reading /etc/group: %w", err)
	}
	entries := make([]engine.KnowledgeEntry, 0, len(sysGroups))
	for name := range sysGroups {
		entries = append(entries, engine.KnowledgeEntry{
			ID: config.NodeID("groups", name),
		})
	}
	return entries, nil
}
