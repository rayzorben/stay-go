package containers

import (
	"context"
	"fmt"
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// BuildUpdatePlan emits one ActionUpgrade node per tracked container: pull the
// configured image and recreate the container only when the image actually
// changed. Containers not yet applied (absent from state) are reported as SKIP
// so the plan explains why they are not being touched.
// Implements engine.Updater.
func (r *Resource) BuildUpdatePlan(_ context.Context, st *state.State) ([]*engine.PlanNode, error) {
	var nodes []*engine.PlanNode
	for i := range r.cfg.Containers {
		entry := &r.cfg.Containers[i]
		name := entry.ContainerName()
		id := config.NodeID("containers", name)
		r.nodeConfigs[id] = entry

		source := entry.SourceFile
		if source == "" {
			source = "common"
		}
		rt := resolveRuntime(entry.Runtime)

		node := &engine.PlanNode{
			ID:           id,
			ResourceType: "containers",
			DisplayName:  name,
			Action:       engine.ActionUpgrade,
			Locked:       entry.Lock,
			NeedsSudo:    entry.Sudo,
			SourceFile:   source,
			Description:  "pull " + entry.Image + " via " + rt + ", recreate if changed",
		}
		if !st.Has(id) {
			node.Action = engine.ActionSkip
			node.SkipReason = "not applied yet — run stay-go first"
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// executeUpgrade pulls the configured image and recreates the container only
// when the pulled image differs from the one the container is running (or the
// container is missing). State is never touched — the desired config is
// unchanged.
func (r *Resource) executeUpgrade(ctx context.Context, entry *config.ContainerEntry) error {
	name := entry.ContainerName()
	rt := resolveRuntime(entry.Runtime)
	opts := executor.Options{Sudo: entry.Sudo}

	result, err := r.exec.Run(ctx, opts, rt, "pull", entry.Image)
	if err != nil {
		return fmt.Errorf("pulling image %q: %w\nstderr: %s", entry.Image, err, result.Stderr)
	}

	if imageID(ctx, r.exec, rt, opts, entry.Image) == containerImageID(ctx, r.exec, rt, opts, name) &&
		containerExists(ctx, r.exec, rt, name) {
		return nil // already running the latest image
	}

	if containerExists(ctx, r.exec, rt, name) {
		if err := stopAndRemove(ctx, r.exec, rt, opts, name); err != nil {
			return err
		}
	}
	return startContainer(ctx, r.exec, rt, opts, entry, name)
}

// imageID returns the local image ID for an image reference, or "" on error.
func imageID(ctx context.Context, exec *executor.Executor, rt string, opts executor.Options, image string) string {
	result, err := exec.Run(ctx, opts, rt, "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil || result == nil {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

// containerImageID returns the image ID a container was created from, or "" if
// the container does not exist.
func containerImageID(ctx context.Context, exec *executor.Executor, rt string, opts executor.Options, name string) string {
	result, err := exec.Run(ctx, opts, rt, "inspect", "--format", "{{.Image}}", name)
	if err != nil || result == nil {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}
