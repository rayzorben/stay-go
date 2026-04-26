package containers

import (
	"context"
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
)

// GatherKnowledge reports which configured containers are currently running.
// A container is "known" only when it exists AND is in the running state.
// Stopped containers are not reported so that the plan will ADD (restart) them.
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(ctx context.Context) ([]engine.KnowledgeEntry, error) {
	var entries []engine.KnowledgeEntry
	for i := range r.cfg.Containers {
		e := &r.cfg.Containers[i]
		name := e.ContainerName()
		rt := resolveRuntime(e.Runtime)

		if isRunning(ctx, r.exec, rt, name) {
			entries = append(entries, engine.KnowledgeEntry{
				ID: config.NodeID("containers", name),
			})
		}
	}
	return entries, nil
}

// isRunning returns true when the named container exists and is running.
func isRunning(ctx context.Context, exec *executor.Executor, runtime, name string) bool {
	result, _ := exec.Run(ctx, executor.Options{},
		runtime, "inspect", "--format", "{{.State.Running}}", name)
	if result == nil || result.ExitCode != 0 {
		return false
	}
	return strings.TrimSpace(result.Stdout) == "true"
}

// containerExists returns true when the named container exists in any state
// (running or stopped). Used by Execute to clean up before re-creating.
func containerExists(ctx context.Context, exec *executor.Executor, runtime, name string) bool {
	result, _ := exec.Run(ctx, executor.Options{},
		runtime, "inspect", "--format", "{{.Name}}", name)
	return result != nil && result.ExitCode == 0
}
