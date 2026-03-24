package services

import (
	"context"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
)

// GatherKnowledge checks the enabled status of every service declared in config
// using `systemctl is-enabled`. Returns a KnowledgeEntry for each enabled service.
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(ctx context.Context) ([]engine.KnowledgeEntry, error) {
	var entries []engine.KnowledgeEntry
	for i := range r.cfg.Services {
		s := &r.cfg.Services[i]
		if isEnabled(ctx, r.exec, s.Service, s.User) {
			entries = append(entries, engine.KnowledgeEntry{
				ID: config.NodeID("services", s.Service),
			})
		}
	}
	return entries, nil
}
