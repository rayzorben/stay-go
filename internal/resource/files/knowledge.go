package files

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
)

// GatherKnowledge checks whether each configured target satisfies desired state.
// Default inline files: target exists (Lstat; symlinks count as present).
// Additive inline content (content + add: true): target contains the snippet.
// Implements engine.Knowledger.
func (r *Resource) GatherKnowledge(ctx context.Context) ([]engine.KnowledgeEntry, error) {
	var entries []engine.KnowledgeEntry
	for i := range r.cfg.Files {
		entry := &r.cfg.Files[i]
		target := entry.Target
		if entry.Content != "" && entry.Add {
			if r.containsSnippet(ctx, target, entry.Content, entry.Sudo) {
				entries = append(entries, engine.KnowledgeEntry{ID: nodeID(target)})
			}
			continue
		}
		if !r.targetExists(ctx, target, entry.Sudo) {
			continue
		}
		// For symlinks, "present" means the link resolves to the intended
		// source. A dangling or wrong-target link — e.g. the repo moved and
		// ${config_root} now points elsewhere, or a manual edit — must be
		// reported as absent so the planner re-creates it. Lstat alone would
		// treat even a broken symlink as present and the node would TRACK.
		if entry.Symlink && entry.Source != "" {
			if dest, ok := r.readlink(ctx, target, entry.Sudo); !ok || dest != entry.Source {
				continue
			}
		}
		entries = append(entries, engine.KnowledgeEntry{ID: nodeID(target)})
	}
	return entries, nil
}

// targetExists reports whether target exists (symlinks count, dangling or not).
// A sudo entry whose parent directory the user cannot search (e.g.
// /etc/sudoers.d, mode 0750) is probed with "sudo -n"; without that, the
// unprivileged Lstat fails with EACCES and the file plans ADD on every run.
func (r *Resource) targetExists(ctx context.Context, target string, sudo bool) bool {
	_, err := os.Lstat(target)
	if err == nil {
		return true
	}
	if !sudo || !errors.Is(err, os.ErrPermission) {
		return false
	}
	_, ok := r.sudoProbe(ctx, "test", "-e", target, "-o", "-L", target)
	return ok
}

func (r *Resource) readlink(ctx context.Context, target string, sudo bool) (string, bool) {
	dest, err := os.Readlink(target)
	if err == nil {
		return dest, true
	}
	if !sudo || !errors.Is(err, os.ErrPermission) {
		return "", false
	}
	out, ok := r.sudoProbe(ctx, "readlink", target)
	return strings.TrimSuffix(out, "\n"), ok
}

func (r *Resource) containsSnippet(ctx context.Context, path, snippet string, sudo bool) bool {
	if snippet == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err == nil {
		return strings.Contains(string(data), snippet)
	}
	if !sudo || !errors.Is(err, os.ErrPermission) {
		return false
	}
	out, ok := r.sudoProbe(ctx, "cat", path)
	return ok && strings.Contains(out, snippet)
}

// sudoProbe runs a read-only command under "sudo -n": it never prompts, so
// knowledge gathering stays non-interactive. Without cached credentials (or a
// NOPASSWD rule) it fails and the caller treats the target as absent.
func (r *Resource) sudoProbe(ctx context.Context, args ...string) (string, bool) {
	if r.exec == nil {
		return "", false
	}
	res, err := r.exec.Run(ctx, executor.Options{}, "sudo", append([]string{"-n"}, args...)...)
	if err != nil {
		return "", false
	}
	return res.Stdout, true
}
