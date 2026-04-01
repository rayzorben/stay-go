package secrets

import (
	"context"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	secretspkg "github.com/rayben/stay-go/internal/secrets"
	"github.com/rayben/stay-go/internal/state"
)

// BuildPlan constructs the plan nodes for the secrets resource.
//
//   - Plaintext entries (not yet encrypted) → ActionAdd with description "encrypt with existing key"
//   - Encrypted entries → ActionTrack or ActionUpdate (hash-based, same as all other resources)
//   - State entries no longer in config → ActionRemove (clears state only; no file modification)
func (r *Resource) BuildPlan(_ context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	var nodes []*engine.PlanNode
	configSet := make(map[string]bool, len(r.cfg.Secrets))

	for key, entry := range r.cfg.Secrets {
		if key == secretspkg.VerifyKey {
			continue
		}
		configSet[key] = true
		nodeID := config.NodeID("secrets", key)
		r.entries[nodeID] = entry

		var node *engine.PlanNode

		if !entry.Encrypted {
			// Plaintext — always ADD. We skip the DetermineAction path because
			// plaintext values are never saved to state; they're transient until
			// the user approves the plan and Execute encrypts them.
			node = &engine.PlanNode{
				ID:           nodeID,
				ResourceType: "secrets",
				DisplayName:  key,
				Action:       engine.ActionAdd,
				Level:        entry.Level,
				ConfigHash:   config.Hash(entry.RawValue),
				Description:  "encrypt with existing key",
			}
		} else {
			configHash := config.Hash(entry.RawValue)
			action := engine.DetermineAction(nodeID, knowledge[nodeID], configHash, st)
			action, desc := engine.CheckLevelChange(nodeID, entry.Level, action, st)
			node = &engine.PlanNode{
				ID:           nodeID,
				ResourceType: "secrets",
				DisplayName:  key,
				Action:       action,
				Level:        entry.Level,
				ConfigHash:   configHash,
				Description:  desc,
			}
		}
		nodes = append(nodes, node)
	}

	// REMOVE nodes for secrets in state that are no longer in config.
	removals := engine.StateRemovals("secrets", configSet, knowledge, st)
	nodes = append(nodes, removals...)

	return nodes, nil
}
