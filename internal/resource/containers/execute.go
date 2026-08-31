package containers

import (
	"context"
	"fmt"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// Execute creates, recreates, or removes a container.
// State is updated on success.
// Implements engine.NodeExecutor.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	entry, ok := r.nodeConfigs[node.ID]
	if !ok {
		// REMOVE nodes for containers deleted from config have no config entry;
		// reconstruct the container name and runtime from persisted state.
		if node.Action == engine.ActionRemove {
			name := node.DisplayName
			rt := resolveRuntime("")
			if se, ok := st.Get(node.ID); ok && se.Data != nil {
				if n, _ := se.Data["name"].(string); n != "" {
					name = n
				}
				if rts, _ := se.Data["runtime"].(string); rts != "" {
					rt = rts
				}
			}
			if containerExists(ctx, r.exec, rt, name) {
				if err := stopAndRemove(ctx, r.exec, rt, executor.Options{}, name); err != nil {
					return err
				}
			}
			st.Delete(node.ID)
			return nil
		}
		return fmt.Errorf("no config found for container %q", node.DisplayName)
	}

	name := entry.ContainerName()
	rt := resolveRuntime(entry.Runtime)
	opts := executor.Options{Sudo: entry.Sudo}

	switch node.Action {
	case engine.ActionUpgrade:
		return r.executeUpgrade(ctx, entry)

	case engine.ActionAdd:
		// Remove any stopped container with the same name before (re)creating.
		if containerExists(ctx, r.exec, rt, name) {
			if err := stopAndRemove(ctx, r.exec, rt, opts, name); err != nil {
				return err
			}
		}
		if err := startContainer(ctx, r.exec, rt, opts, entry, name); err != nil {
			return err
		}
		st.Set(node.ID, node.ConfigHash, node.SourceFile, node.StateData)

	case engine.ActionUpdate:
		// Recreate: stop + remove the existing container, then run a fresh one.
		if containerExists(ctx, r.exec, rt, name) {
			if err := stopAndRemove(ctx, r.exec, rt, opts, name); err != nil {
				return err
			}
		}
		if err := startContainer(ctx, r.exec, rt, opts, entry, name); err != nil {
			return err
		}
		st.Set(node.ID, node.ConfigHash, node.SourceFile, node.StateData)

	case engine.ActionRemove:
		if containerExists(ctx, r.exec, rt, name) {
			if err := stopAndRemove(ctx, r.exec, rt, opts, name); err != nil {
				return err
			}
		}
		st.Delete(node.ID)
	}

	return nil
}

// startContainer runs `runtime run -d ...` to create and start a container.
func startContainer(ctx context.Context, exec *executor.Executor, rt string, opts executor.Options, entry *config.ContainerEntry, name string) error {
	args := buildRunArgs(entry, name)
	result, err := exec.Run(ctx, opts, rt, args...)
	if err != nil {
		return fmt.Errorf("starting container %q: %w\nstderr: %s", name, err, result.Stderr)
	}
	return nil
}

// stopAndRemove stops a running container (if needed) then removes it.
func stopAndRemove(ctx context.Context, exec *executor.Executor, rt string, opts executor.Options, name string) error {
	// Stop is best-effort; the container may already be stopped.
	exec.Run(ctx, opts, rt, "stop", name) //nolint:errcheck

	result, err := exec.Run(ctx, opts, rt, "rm", name)
	if err != nil {
		return fmt.Errorf("removing container %q: %w\nstderr: %s", name, err, result.Stderr)
	}
	return nil
}
