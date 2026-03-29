package users

import (
	"context"
	"fmt"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
)

// GatherKnowledge reads /etc/passwd (no external commands) and returns a
// KnowledgeEntry for every system user. Using the native file avoids spawning
// processes and ensures correctness in containers and non-standard environments.
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(_ context.Context) ([]engine.KnowledgeEntry, error) {
	sysUsers, err := parsePasswd()
	if err != nil {
		return nil, fmt.Errorf("reading /etc/passwd: %w", err)
	}
	entries := make([]engine.KnowledgeEntry, 0, len(sysUsers))
	for name := range sysUsers {
		entries = append(entries, engine.KnowledgeEntry{
			ID: config.NodeID("users", name),
		})
	}
	return entries, nil
}
