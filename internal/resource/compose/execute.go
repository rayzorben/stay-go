package compose

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// Execute brings a compose project up (ADD/UPDATE) or down (REMOVE).
// State is updated on success.
// Implements engine.NodeExecutor.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	switch node.Action {
	case engine.ActionUpgrade:
		entry, ok := r.nodeConfigs[node.ID]
		if !ok {
			return fmt.Errorf("no config found for compose project %q", node.DisplayName)
		}
		return r.executeUpgrade(ctx, entry)

	case engine.ActionAdd, engine.ActionUpdate:
		entry, ok := r.nodeConfigs[node.ID]
		if !ok {
			return fmt.Errorf("no config found for compose project %q", node.DisplayName)
		}
		return r.up(ctx, entry, node, st)

	case engine.ActionRemove:
		entry := r.nodeConfigs[node.ID]
		if entry == nil {
			entry = entryFromState(node, st)
		}
		return r.down(ctx, entry, node, st)
	}
	return nil
}

// up renders the project and runs `<runtime> compose up -d --remove-orphans`.
func (r *Resource) up(ctx context.Context, entry *config.ComposeEntry, node *engine.PlanNode, st *state.State) error {
	rt := resolveRuntime(entry.Runtime)
	dir, err := renderProject(entry, r.cfg)
	if err != nil {
		return err
	}
	args := append(composeArgs(dir, entry), "up", "-d", "--remove-orphans")
	result, err := r.exec.Run(ctx, executor.Options{Sudo: entry.Sudo}, rt, args...)
	if err != nil {
		return fmt.Errorf("%s compose up %q: %w\nstderr: %s", rt, entry.ProjectName(), err, result.Stderr)
	}
	st.Set(node.ID, node.ConfigHash, node.SourceFile, node.StateData)
	return nil
}

// down runs `<runtime> compose down --remove-orphans` for the project, rendering
// the source directory when it still exists (so compose has the file list), or
// tearing down by project name (label-based) when the source is gone.
func (r *Resource) down(ctx context.Context, entry *config.ComposeEntry, node *engine.PlanNode, st *state.State) error {
	rt := resolveRuntime(entry.Runtime)
	opts := executor.Options{Sudo: entry.Sudo}

	var args []string
	if entry.Path != "" && isDir(entry.Path) {
		dir, err := renderProject(entry, r.cfg)
		if err != nil {
			return err
		}
		args = append(composeArgs(dir, entry), "down", "--remove-orphans")
	} else {
		args = []string{"compose", "-p", entry.ProjectName(), "down", "--remove-orphans"}
	}

	result, err := r.exec.Run(ctx, opts, rt, args...)
	if err != nil {
		return fmt.Errorf("%s compose down %q: %w\nstderr: %s", rt, entry.ProjectName(), err, result.Stderr)
	}
	if dir, derr := projectDir(entry.ProjectName()); derr == nil {
		os.RemoveAll(dir) //nolint:errcheck — best-effort cleanup of rendered files
	}
	st.Delete(node.ID)
	return nil
}

// entryFromState reconstructs a minimal ComposeEntry for a REMOVE node whose
// config entry no longer exists, using the data persisted in state.
func entryFromState(node *engine.PlanNode, st *state.State) *config.ComposeEntry {
	e := &config.ComposeEntry{Project: node.DisplayName}
	if se, ok := st.Get(node.ID); ok {
		if v, _ := se.Data["path"].(string); v != "" {
			e.Path = v
		}
		if v, _ := se.Data["runtime"].(string); v != "" {
			e.Runtime = v
		}
	}
	return e
}

// renderProject (re)materialises the compose project at entry.Path into a stable
// per-project directory under the user cache, substituting tokens in every file
// (same substitution as config string fields, plus ${secrets.x} → plaintext).
//
// A stable path — not a fresh temp dir — is used so that `docker compose` sees
// an unchanging project directory and does not churn containers between runs.
// The directory and its files are owner-only (0700/0600), so the rendered .env
// holding the plaintext secret is not readable by other users.
func renderProject(entry *config.ComposeEntry, cfg *config.Config) (string, error) {
	dst, err := projectDir(entry.ProjectName())
	if err != nil {
		return "", err
	}
	if err := os.RemoveAll(dst); err != nil {
		return "", fmt.Errorf("clearing rendered compose dir: %w", err)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return "", fmt.Errorf("creating rendered compose dir: %w", err)
	}
	err = filepath.WalkDir(entry.Path, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(entry.Path, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks, devices, sockets, …
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, []byte(config.RenderExternal(string(raw), cfg)), 0o600)
	})
	if err != nil {
		return "", fmt.Errorf("rendering compose project %q: %w", entry.ProjectName(), err)
	}
	return dst, nil
}

// projectDir returns the stable rendered-project directory for a compose project.
func projectDir(project string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "stay-go", "compose", project), nil
}
