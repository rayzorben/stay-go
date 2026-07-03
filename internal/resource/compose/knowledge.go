package compose

import (
	"context"
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
)

// GatherKnowledge reports which configured compose projects currently have at
// least one running container. Fully-stopped projects are not reported, so the
// plan will (re)run `compose up -d` for them.
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(ctx context.Context) ([]engine.KnowledgeEntry, error) {
	var entries []engine.KnowledgeEntry
	for i := range r.cfg.Compose {
		e := &r.cfg.Compose[i]
		if projectRunning(ctx, r.exec, resolveRuntime(e.Runtime), e.ProjectName()) {
			entries = append(entries, engine.KnowledgeEntry{ID: config.NodeID("compose", e.ProjectName())})
		}
	}
	return entries, nil
}

// projectRunning returns true when the named compose project has any running
// container. It queries the runtime by label, so it works for both docker
// compose and podman compose without parsing their differing `ls` output.
func projectRunning(ctx context.Context, exec *executor.Executor, runtime, project string) bool {
	result, err := exec.Run(ctx, executor.Options{},
		runtime, "ps", "-q", "--filter", projectLabelFilter(project))
	if err != nil || result == nil || result.ExitCode != 0 {
		return false
	}
	return strings.TrimSpace(result.Stdout) != ""
}
