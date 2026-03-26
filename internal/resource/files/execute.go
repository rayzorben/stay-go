package files

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/executor"
	"github.com/rayben/stay-go/internal/state"
)

// Execute carries out the planned action for a single file node.
// State is updated on success.
// Implements engine.NodeExecutor.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	switch node.Action {
	case engine.ActionAdd, engine.ActionUpdate:
		entry := r.nodeConfigs[node.ID]
		if entry == nil {
			return fmt.Errorf("no config found for file %q", node.DisplayName)
		}
		if err := r.apply(ctx, entry); err != nil {
			return err
		}
		st.Set(node.ID, node.ConfigHash, node.Level, nil)

	case engine.ActionRemove:
		// State-only removal — leaves files on disk untouched (safe default).
		st.Delete(node.ID)
	}
	return nil
}

// apply performs the actual file placement for the given entry.
func (r *Resource) apply(ctx context.Context, entry *config.FileEntry) error {
	target := entry.Target
	kind := detectKind(entry.Source)

	switch kind {

	case kindSecret:
		key := secretKey(entry.Source)
		content, ok := r.cfg.DecryptedSecrets[key]
		if !ok {
			return fmt.Errorf("secret %q not available (not decrypted)", key)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("creating directory for %q: %w", target, err)
		}
		if err := atomicWriteFile(target, []byte(content), 0o600); err != nil {
			return fmt.Errorf("writing %q: %w", target, err)
		}

	case kindLocal:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("creating directory for %q: %w", target, err)
		}
		if entry.Symlink {
			// Remove stale link/file first so the symlink can be (re)created.
			_ = os.Remove(target)
			if err := os.Symlink(entry.Source, target); err != nil {
				return fmt.Errorf("creating symlink %q -> %q: %w", target, entry.Source, err)
			}
		} else {
			if err := copyFile(entry.Source, target); err != nil {
				return fmt.Errorf("copying %q to %q: %w", entry.Source, target, err)
			}
		}

	case kindGitSSH:
		sshCmd := buildSSHCommand(entry.SSHKey)
		env := []string{"GIT_SSH_COMMAND=" + sshCmd}
		if err := r.gitCloneOrPull(ctx, entry.Source, target, executor.Options{
			Env:   env,
			Stdin: os.Stdin, // allow SSH passphrase prompt via /dev/tty
		}); err != nil {
			return err
		}

	case kindGitHTTPS:
		if err := r.gitCloneOrPull(ctx, entry.Source, target, executor.Options{}); err != nil {
			return err
		}

	case kindHTTP:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("creating directory for %q: %w", target, err)
		}
		if err := downloadFile(ctx, entry.Source, target); err != nil {
			return fmt.Errorf("downloading %q to %q: %w", entry.Source, target, err)
		}
	}

	if entry.Mode != "" {
		if err := applyMode(ctx, r.exec, target, entry.Mode, entry.Sudo); err != nil {
			return err
		}
	}
	return nil
}

// gitCloneOrPull clones url into target, or pulls if target already contains a git repo.
// A context line is printed to stderr before the operation so that any SSH passphrase
// prompt (which arrives via /dev/tty and overwrites the \r progress line) is preceded
// by visible context about which repository triggered it.
func (r *Resource) gitCloneOrPull(ctx context.Context, url, target string, opts executor.Options) error {
	gitDir := filepath.Join(target, ".git")
	if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
		fmt.Fprintf(os.Stderr, "\n  ↳ pulling %s\n", url)
		result, err := r.exec.Run(ctx, opts, "git", "-C", target, "pull")
		if err != nil {
			return fmt.Errorf("git pull in %q: %w\nstderr: %s", target, err, result.Stderr)
		}
		return nil
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("target %q already exists but is not a git repository", target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("creating parent directory for %q: %w", target, err)
	}
	fmt.Fprintf(os.Stderr, "\n  ↳ cloning %s → %s\n", url, target)
	result, err := r.exec.Run(ctx, opts, "git", "clone", url, target)
	if err != nil {
		return fmt.Errorf("git clone %q into %q: %w\nstderr: %s", url, target, err, result.Stderr)
	}
	return nil
}

// buildSSHCommand constructs the GIT_SSH_COMMAND value for the given key paths.
func buildSSHCommand(keys []string) string {
	parts := []string{"ssh", "-o", "StrictHostKeyChecking=accept-new"}
	for _, k := range keys {
		parts = append(parts, "-i", k)
	}
	return strings.Join(parts, " ")
}

// applyMode runs chmod to set the mode on path.
func applyMode(ctx context.Context, exec *executor.Executor, path, mode string, sudo bool) error {
	result, err := exec.Run(ctx, executor.Options{Sudo: sudo}, "chmod", mode, path)
	if err != nil {
		return fmt.Errorf("chmod %q %q: %w\nstderr: %s", mode, path, err, result.Stderr)
	}
	return nil
}

// atomicWriteFile writes content to path via a temp file + rename, with the given permissions.
func atomicWriteFile(path string, content []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// copyFile copies src to dst atomically, preserving source permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// downloadFile fetches url and writes it to path atomically.
func downloadFile(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
