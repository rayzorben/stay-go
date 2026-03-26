package files

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/state"
)

// BuildPlan computes PlanNodes for managed files.
//
// Hash strategy:
//   - Secret sources: use the ciphertext (stable until secret changes)
//   - Local files: hash the file content (changes trigger UPDATE)
//   - Git/HTTP: hash the URL + config (content not inspected at plan time)
//
// Secret sources add an implicit DependsOn the secrets node.
// Implements engine.Planner.
func (r *Resource) BuildPlan(_ context.Context, knowledge map[string]bool, st *state.State) ([]*engine.PlanNode, error) {
	configSet := make(map[string]bool, len(r.cfg.Files))
	var nodes []*engine.PlanNode

	for i := range r.cfg.Files {
		entry := &r.cfg.Files[i]
		target := entry.Target
		configSet[target] = true

		id := nodeID(target)
		displayName := filepath.Base(target)
		level := entry.Level
		if level == "" {
			level = "common"
		}

		kind := detectKind(entry.Source)

		hash, skipReason := computeHash(entry, kind, r.cfg)
		if skipReason != "" {
			nodes = append(nodes, &engine.PlanNode{
				ID:           id,
				ResourceType: "files",
				DisplayName:  displayName,
				Action:       engine.ActionSkip,
				SkipReason:   skipReason,
				Level:        level,
			})
			continue
		}

		action := engine.DetermineAction(id, knowledge[id], hash, st)
		// ADOPT means "target exists but not tracked" — promote to ADD so the
		// file is properly placed under stay-go management.
		if action == engine.ActionAdopt {
			action = engine.ActionAdd
		}
		action, levelDesc := engine.CheckLevelChange(id, level, action, st)

		desc := describeAction(action, kind)
		if levelDesc != "" {
			desc = levelDesc
		}

		// Build dependency list: declared deps + implicit secret dep.
		deps := entry.DependsOnIDs()
		if kind == kindSecret {
			deps = append(deps, config.NodeID("secrets", secretKey(entry.Source)))
		}

		r.nodeConfigs[id] = entry
		nodes = append(nodes, &engine.PlanNode{
			ID:           id,
			ResourceType: "files",
			DisplayName:  displayName,
			Action:       action,
			ConfigHash:   hash,
			DependsOn:    deps,
			NeedsSudo:    entry.Sudo,
			Level:        level,
			Description:  desc,
		})
	}

	nodes = append(nodes, engine.StateRemovals("files", configSet, st)...)
	return nodes, nil
}

// computeHash returns the config hash for the entry, plus a non-empty skip
// reason if the source cannot be hashed (missing local file, unknown secret).
func computeHash(entry *config.FileEntry, kind sourceKind, cfg *config.Config) (string, string) {
	type data struct {
		Source  string
		Target  string
		Mode    string
		SSHKey  []string
		Symlink bool
	}
	source := entry.Source
	switch kind {
	case kindSecret:
		key := secretKey(entry.Source)
		se, ok := cfg.Secrets[key]
		if !ok {
			return "", fmt.Sprintf("secret %q not found in config", key)
		}
		if se.Encrypted {
			source = se.RawValue // ciphertext; stable until secret is changed
		}
		// plaintext (not yet encrypted): use raw value as-is
	case kindLocal:
		content, err := os.ReadFile(entry.Source)
		if err != nil {
			return "", fmt.Sprintf("source file not found: %s", entry.Source)
		}
		source = string(content)
		// git/http: hash the URL+config only; content is not inspected at plan time
	}
	return config.Hash(data{
		Source:  source,
		Target:  entry.Target,
		Mode:    entry.Mode,
		SSHKey:  entry.SSHKey,
		Symlink: entry.Symlink,
	}), ""
}

// describeAction returns a short description of what the action will do.
func describeAction(action engine.ActionType, kind sourceKind) string {
	switch action {
	case engine.ActionAdd:
		switch kind {
		case kindSecret:
			return "write secret to file"
		case kindLocal:
			return "copy file"
		case kindGitSSH:
			return "clone repository (SSH)"
		case kindGitHTTPS:
			return "clone repository"
		case kindHTTP:
			return "download file"
		}
	case engine.ActionUpdate:
		return "source changed, update"
	case engine.ActionRemove:
		return "remove from tracking"
	}
	return ""
}
