package files

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
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
		if target == "" {
			label := entry.Source
			if entry.Content != "" {
				snippet := strings.TrimSpace(entry.Content)
				if len(snippet) > 40 {
					snippet = snippet[:40] + "…"
				}
				label = fmt.Sprintf("(inline: %s)", snippet)
			}
			nodes = append(nodes, &engine.PlanNode{
				ID:           fmt.Sprintf("files/__no_target_%d__", i),
				ResourceType: "files",
				DisplayName:  label,
				Action:       engine.ActionSkip,
				SkipReason:   "missing target",
			})
			continue
		}
		configSet[target] = true

		id := nodeID(target)
		displayName := target // full path; display.go truncates to fit terminal
		level := entry.SourceFile
		if level == "" {
			level = "common"
		}

		kind := detectKind(entry)

		hash, skipReason := computeHash(entry, kind, r.cfg)
		if skipReason != "" {
			nodes = append(nodes, &engine.PlanNode{
				ID:           id,
				ResourceType: "files",
				DisplayName:  displayName,
				Action:       engine.ActionSkip,
				SkipReason:   skipReason,
				SourceFile: level,
			})
			continue
		}

		action := engine.DetermineAction(id, knowledge[id], hash, st)
		// Additive inline content counts as "in knowledge" only when the file
		// already contains the current snippet. After a config edit the new text
		// is not in the file yet, so DetermineAction returns ADD even though this
		// node is already tracked — upgrade to UPDATE so Execute removes the
		// previous snippet and appends the new one.
		if kind == kindInline && entry.Add && action == engine.ActionAdd {
			if e, ok := st.Get(id); ok && e.Hash != hash {
				action = engine.ActionUpdate
			}
		}
		// ADOPT means "target exists but not tracked" — promote to ADD so the
		// file is properly placed under stay-go management.
		if action == engine.ActionAdopt {
			action = engine.ActionAdd
		}
		action, levelDesc := engine.CheckLevelChange(id, level, action, st)

		// Secret dependencies are wired globally by the engine (every resource
		// runs after the secrets resource); no per-entry secret deps needed.
		deps := entry.DependsOnIDs()

		notes := buildFileNotes(action, kind, entry)
		if levelDesc != "" {
			notes = []string{levelDesc}
		}

		var stateData map[string]interface{}
		if kind == kindInline && entry.Add {
			stateData = map[string]interface{}{
				stateKeyAdditive: true,
				stateKeySnippet:  entry.Content,
				stateKeySudo:     entry.Sudo,
			}
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
			SourceFile: level,
			Notes:        notes,
			StateData:    stateData,
		})
	}

	// knowledge only covers configured targets, so it cannot confirm absence —
	// pass nil or the engine would skip Execute on removal and additive
	// snippets would never be cleaned out of their target files.
	nodes = append(nodes, engine.StateRemovals("files", configSet, nil, st)...)
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
		Add     bool
	}
	source := entry.Source
	switch kind {
	case kindInline:
		source = entry.Content // already secrets→ciphertext-resolved by the config pipeline
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
		Add:     entry.Add,
	}), ""
}

// buildFileNotes returns the ↳ sub-line detail for a file plan node.
// REMOVE and LEVEL nodes get no notes — the action label is self-explanatory.
func buildFileNotes(action engine.ActionType, kind sourceKind, entry *config.FileEntry) []string {
	switch action {
	case engine.ActionRemove, engine.ActionLevel, engine.ActionTrack:
		return nil
	}
	switch kind {
	case kindLocal:
		if entry.Symlink {
			return []string{"symlink " + entry.Target + " → " + entry.Source}
		}
		return []string{"copy " + entry.Source + " to " + entry.Target}
	case kindHTTP:
		return []string{"download " + entry.Source}
	case kindGitSSH:
		return []string{"clone " + entry.Source + " (SSH)"}
	case kindGitHTTPS:
		return []string{"clone " + entry.Source}
	case kindSecret:
		return []string{"write secret to " + entry.Target}
	case kindInline:
		if entry.Add {
			return []string{"ensure content present in " + entry.Target}
		}
		return []string{"write inline content to " + entry.Target}
	}
	return nil
}
