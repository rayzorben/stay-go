package secrets

import (
	"context"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	secretspkg "github.com/rayzorben/stay-go/internal/secrets"
)

// GatherKnowledge reports all configured secret keys as "present" (same
// pattern as scripts: the engine never queries the system for secret
// existence; the config and state are the sole source of truth).
func (r *Resource) GatherKnowledge(_ context.Context) ([]engine.KnowledgeEntry, error) {
	var entries []engine.KnowledgeEntry
	for key := range r.cfg.Secrets {
		if key == secretspkg.VerifyKey {
			continue
		}
		entries = append(entries, engine.KnowledgeEntry{
			ID: config.NodeID("secrets", key),
		})
	}
	return entries, nil
}
