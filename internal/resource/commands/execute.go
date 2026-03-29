package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/executor"
	"github.com/rayben/stay-go/internal/state"
)

// Execute runs the command, re-runs it on update, or executes the rollback on removal.
// State is updated on success.
// Implements engine.NodeExecutor.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	switch node.Action {
	case engine.ActionAdd, engine.ActionUpdate:
		entry, ok := r.nodeConfigs[node.ID]
		if !ok {
			return fmt.Errorf("no config found for command %q", node.DisplayName)
		}
		// Prepend set -e so bash exits on the first failing statement.
		// Without this, bash -c exits with the last command's code, masking
		// earlier failures in multi-line commands.
		// Substitute ${secrets.*} tokens with decrypted values at the last
		// moment before execution so plaintext never appears in plan output.
		cmd := config.ApplySecrets(entry.Command, r.cfg.DecryptedSecrets)
		result, err := r.exec.Run(ctx, executor.Options{Sudo: entry.Sudo, AllowInteractive: true}, "bash", "-c", "set -e\n"+cmd)
		if err != nil {
			return fmt.Errorf("running command %q: %w\nstderr: %s", node.DisplayName, err, result.Stderr)
		}
		st.Set(node.ID, node.ConfigHash, node.Level, node.StateData)

	case engine.ActionRemove:
		// Retrieve stored rollback from state data, falling back to live config if present.
		rollback := ""
		if stateEntry, ok := st.Get(node.ID); ok && stateEntry.Data != nil {
			rollback, _ = stateEntry.Data["rollback"].(string)
		} else if entry, ok := r.nodeConfigs[node.ID]; ok {
			rollback = entry.Rollback
		}
		rollback = strings.TrimSpace(rollback)
		if rollback != "" {
			// Determine sudo from stored state data.
			sudo := false
			if stateEntry, ok := st.Get(node.ID); ok && stateEntry.Data != nil {
				sudo, _ = stateEntry.Data["sudo"].(bool)
			}
			rollback = config.ApplySecrets(rollback, r.cfg.DecryptedSecrets)
			result, err := r.exec.Run(ctx, executor.Options{Sudo: sudo}, "bash", "-c", "set -e\n"+rollback)
			if err != nil {
				return fmt.Errorf("rollback for command %q: %w\nstderr: %s", node.DisplayName, err, result.Stderr)
			}
		}
		st.Delete(node.ID)
	}

	return nil
}
