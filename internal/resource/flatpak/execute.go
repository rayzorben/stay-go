package flatpak

import (
	"context"
	"fmt"
	"strings"

	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// Execute adds or removes a single Flatpak remote or application.
// State is updated on success. Implements engine.NodeExecutor.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	switch node.Action {
	case engine.ActionUpgrade:
		return r.executeUpgrade(ctx, node)

	case engine.ActionAdd, engine.ActionUpdate:
		if isRemoteNode(node.ID) {
			return r.applyRemote(ctx, node, st)
		}
		return r.applyApp(ctx, node, st)
	case engine.ActionRemove:
		if isRemoteNode(node.ID) {
			return r.deleteRemote(ctx, node, st)
		}
		return r.deleteApp(ctx, node, st)
	}
	return nil
}

func (r *Resource) applyRemote(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	name, _ := node.StateData["name"].(string)
	url, _ := node.StateData["url"].(string)

	if node.Action == engine.ActionUpdate {
		// Delete and re-add to apply a URL change.
		result, err := r.exec.Run(ctx, executor.Options{}, "flatpak", "remote-delete", "--user", name)
		if err != nil {
			return fmt.Errorf("removing flatpak remote %q for update: %w\nstderr: %s", name, err, result.Stderr)
		}
	}

	result, err := r.exec.Run(ctx, executor.Options{}, "flatpak", "remote-add", "--if-not-exists", "--user", name, url)
	if err != nil {
		return fmt.Errorf("adding flatpak remote %q: %w\nstderr: %s", name, err, result.Stderr)
	}
	st.Set(node.ID, node.ConfigHash, node.SourceFile, node.StateData)
	return nil
}

func (r *Resource) deleteRemote(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	if node.AbsentFromSystem {
		st.Delete(node.ID)
		return nil
	}
	name := strings.TrimPrefix(node.ID, remotePrefix)
	result, err := r.exec.Run(ctx, executor.Options{}, "flatpak", "remote-delete", "--user", name)
	if err != nil {
		return fmt.Errorf("removing flatpak remote %q: %w\nstderr: %s", name, err, result.Stderr)
	}
	st.Delete(node.ID)
	return nil
}

func (r *Resource) applyApp(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	appID, _ := node.StateData["app"].(string)
	remote, _ := node.StateData["remote"].(string)

	if node.Action == engine.ActionUpdate {
		// Uninstall first so a remote change takes effect cleanly.
		result, err := r.exec.Run(ctx, executor.Options{}, "flatpak", "uninstall", "--noninteractive", "--user", appID)
		if err != nil {
			return fmt.Errorf("uninstalling flatpak app %q for update: %w\nstderr: %s", appID, err, result.Stderr)
		}
	}

	result, err := r.exec.Run(ctx, executor.Options{}, "flatpak", "install", "--noninteractive", "--user", remote, appID)
	if err != nil {
		return fmt.Errorf("installing flatpak app %q: %w\nstderr: %s", appID, err, result.Stderr)
	}
	st.Set(node.ID, node.ConfigHash, node.SourceFile, node.StateData)
	return nil
}

func (r *Resource) deleteApp(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	if node.AbsentFromSystem {
		st.Delete(node.ID)
		return nil
	}
	appID := strings.TrimPrefix(node.ID, appPrefix)
	result, err := r.exec.Run(ctx, executor.Options{}, "flatpak", "uninstall", "--noninteractive", "--user", appID)
	if err != nil {
		return fmt.Errorf("removing flatpak app %q: %w\nstderr: %s", appID, err, result.Stderr)
	}
	st.Delete(node.ID)
	return nil
}
