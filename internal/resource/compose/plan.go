package compose

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
)

// BuildPlan computes PlanNodes for managed compose projects.
//
// The hash covers the rendered contents of every file under the project
// directory, so any change there triggers a `compose up -d` recreate. "Rendered"
// here means ${var}/${env:}/$(cmd)/~ resolved and ${secrets.x} replaced with the
// secret's ciphertext — plaintext secrets never enter the hash.
//
// Uses engine.DetermineAction (DRY) and engine.StateRemovals (DRY).
// Implements engine.Planner.
func (r *Resource) BuildPlan(_ context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	configSet := make(map[string]bool, len(r.cfg.Compose))
	var nodes []*engine.PlanNode

	for i := range r.cfg.Compose {
		entry := &r.cfg.Compose[i]
		project := entry.ProjectName()
		configSet[project] = true

		id := config.NodeID("compose", project)
		r.nodeConfigs[id] = entry

		source := entry.SourceFile
		if source == "" {
			source = "common"
		}

		if !isDir(entry.Path) {
			nodes = append(nodes, skipNode(id, project, source, "compose path not found: "+entry.Path))
			continue
		}

		hash, err := composeHash(entry, r.cfg)
		if err != nil {
			nodes = append(nodes, skipNode(id, project, source, "reading compose project: "+err.Error()))
			continue
		}

		action := engine.DetermineAction(id, knowledge[id], hash, st)
		action, sourceDesc := engine.CheckSourceChange(id, source, action, st)

		rt := resolveRuntime(entry.Runtime)
		deps := append(entry.DependsOnIDs(), config.NodeID("packages", rt))

		nodes = append(nodes, &engine.PlanNode{
			ID:           id,
			ResourceType: "compose",
			DisplayName:  project,
			Action:       action,
			ConfigHash:   hash,
			DependsOn:    deps,
			NeedsSudo:    entry.Sudo,
			SourceFile:   source,
			Description:  describeChange(rt, action, sourceDesc),
			StateData: map[string]interface{}{
				"project": project,
				"path":    entry.Path,
				"runtime": rt,
			},
		})
	}

	nodes = append(nodes, engine.StateRemovals("compose", configSet, knowledge, st)...)
	return nodes, nil
}

func skipNode(id, name, source, reason string) *engine.PlanNode {
	return &engine.PlanNode{
		ID:           id,
		ResourceType: "compose",
		DisplayName:  name,
		Action:       engine.ActionSkip,
		SkipReason:   reason,
		SourceFile:   source,
	}
}

func describeChange(runtime string, action engine.ActionType, sourceDesc string) string {
	if sourceDesc != "" {
		return sourceDesc
	}
	switch action {
	case engine.ActionAdd:
		return runtime + " compose up -d"
	case engine.ActionUpdate:
		return "recreate changed services (" + runtime + " compose up -d)"
	case engine.ActionRemove:
		return runtime + " compose down"
	}
	return ""
}

// composeHash returns a deterministic hash of the project: its identity fields
// plus the rendered content of every regular file under entry.Path. Secrets are
// substituted as ciphertext (stable until the secret changes), never plaintext.
func composeHash(entry *config.ComposeEntry, cfg *config.Config) (string, error) {
	type file struct{ Rel, Body string }
	var files []file
	err := filepath.WalkDir(entry.Path, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(entry.Path, p)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files = append(files, file{filepath.ToSlash(rel), config.RenderExternalForHash(string(raw), cfg)})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	return config.Hash(struct {
		Project string
		Files   []string
		EnvFile string
		Sudo    bool
		Content []file
	}{entry.ProjectName(), entry.Files, entry.EnvFile, entry.Sudo, files}), nil
}
