package json

import (
	"context"
	"fmt"

	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
)

// Execute applies or reverts JSON value overrides for a single plan node.
// On ADD/UPDATE: reads the current JSON file, applies desired values, and writes it back.
// On REMOVE: reads the original values from state and restores them.
// State is updated on success.
// Implements engine.NodeExecutor.
func (r *Resource) Execute(_ context.Context, node *engine.PlanNode, st *state.State) error {
	switch node.Action {
	case engine.ActionAdd, engine.ActionUpdate:
		entry, ok := r.nodeConfigs[node.ID]
		if !ok {
			return fmt.Errorf("no config found for json entry %q", node.DisplayName)
		}

		doc, err := readFile(entry.File)
		if err != nil {
			return fmt.Errorf("reading %q: %w", entry.File, err)
		}
		if doc == nil {
			return fmt.Errorf("file not found: %s", entry.File)
		}

		// Preserve originals from state if they were stored in a previous run.
		// Only store originals once (the first time we see each path).
		originals := make(map[string]interface{})
		if stateEntry, ok := st.Get(node.ID); ok && stateEntry.Data != nil {
			if saved, ok := stateEntry.Data["originals"].(map[string]interface{}); ok {
				for k, v := range saved {
					originals[k] = v
				}
			}
		}
		for path := range entry.Values {
			if _, alreadySaved := originals[path]; !alreadySaved {
				if v, exists := getPath(doc, path); exists {
					originals[path] = v
				}
			}
		}

		// Apply desired values.
		for path, val := range entry.Values {
			if err := setPath(doc, path, val); err != nil {
				return fmt.Errorf("setting %q in %q: %w", path, entry.File, err)
			}
		}

		if err := writeFile(entry.File, doc); err != nil {
			return err
		}

		stateData := map[string]interface{}{
			"file":      entry.File,
			"values":    entry.Values,
			"originals": originals,
		}
		st.Set(node.ID, node.ConfigHash, node.SourceFile, stateData)

	case engine.ActionRemove:
		// Restore original values from state.
		stateEntry, ok := st.Get(node.ID)
		if !ok || stateEntry.Data == nil {
			// Nothing to restore — just remove from state.
			st.Delete(node.ID)
			return nil
		}

		filePath, _ := stateEntry.Data["file"].(string)
		if filePath == "" {
			st.Delete(node.ID)
			return nil
		}

		originals, _ := stateEntry.Data["originals"].(map[string]interface{})
		if len(originals) == 0 {
			st.Delete(node.ID)
			return nil
		}

		doc, err := readFile(filePath)
		if err != nil {
			return fmt.Errorf("reading %q for restore: %w", filePath, err)
		}
		if doc == nil {
			// File no longer exists — nothing to restore.
			st.Delete(node.ID)
			return nil
		}

		for path, val := range originals {
			if err := setPath(doc, path, val); err != nil {
				return fmt.Errorf("restoring %q in %q: %w", path, filePath, err)
			}
		}

		if err := writeFile(filePath, doc); err != nil {
			return err
		}

		st.Delete(node.ID)
	}

	return nil
}
