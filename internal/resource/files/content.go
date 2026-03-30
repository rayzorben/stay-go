package files

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rayben/stay-go/internal/executor"
)

// state keys for additive (append-if-missing) inline files — read on REMOVE.
const (
	stateKeyAdditive = "additive"
	stateKeySnippet  = "snippet"
	stateKeySudo     = "sudo"
)

func fileContainsSnippet(path, snippet string) bool {
	if snippet == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), snippet)
}

func readFileForEdit(ctx context.Context, exec *executor.Executor, path string, sudo bool) ([]byte, error) {
	if !sudo {
		b, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, os.ErrNotExist
			}
			return nil, err
		}
		return b, nil
	}
	if _, err := exec.Run(ctx, executor.Options{Sudo: true}, "test", "-f", path); err != nil {
		return nil, os.ErrNotExist
	}
	res, err := exec.Run(ctx, executor.Options{Sudo: true}, "cat", path)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w\nstderr: %s", path, err, res.Stderr)
	}
	return []byte(res.Stdout), nil
}

// appendSnippetIfMissing creates or updates path so snippet is present as a
// substring (idempotent). New files use perm; appended content is separated from
// existing text with a newline when the file is non-empty and does not end with one.
func appendSnippetIfMissing(ctx context.Context, exec *executor.Executor, path, snippet string, perm os.FileMode, sudo bool) error {
	if snippet == "" {
		return fmt.Errorf("additive file %q: empty content", path)
	}
	var body []byte
	exists := true
	b, err := readFileForEdit(ctx, exec, path, sudo)
	switch {
	case err == nil:
		body = b
	case errors.Is(err, os.ErrNotExist):
		exists = false
		body = nil
	default:
		return err
	}
	if exists && strings.Contains(string(body), snippet) {
		return nil
	}
	if !exists {
		if err := mkdirAll(ctx, exec, filepath.Dir(path), 0o755, sudo); err != nil {
			return fmt.Errorf("creating directory for %q: %w", path, err)
		}
	}
	var out []byte
	if !exists || len(body) == 0 {
		out = []byte(snippet)
	} else {
		out = append([]byte(nil), body...)
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, []byte(snippet)...)
	}
	if sudo {
		tmp, err := writeTempFile(out, perm)
		if err != nil {
			return fmt.Errorf("writing temp for %q: %w", path, err)
		}
		defer os.Remove(tmp)
		if err := sudoCopy(ctx, exec, tmp, path); err != nil {
			return fmt.Errorf("writing %q: %w", path, err)
		}
		return nil
	}
	return atomicWriteFile(path, out, perm)
}

// removeSnippetFromFile deletes all occurrences of snippet from path. If the
// file is empty or only whitespace afterward, the file is removed.
func removeSnippetFromFile(ctx context.Context, exec *executor.Executor, path, snippet string, sudo bool) error {
	if snippet == "" {
		return nil
	}
	body, err := readFileForEdit(ctx, exec, path, sudo)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	next := strings.ReplaceAll(string(body), snippet, "")
	next = strings.TrimRight(next, "\n")
	next = strings.TrimLeft(next, "\n")
	if strings.TrimSpace(next) == "" {
		if sudo {
			res, err := exec.Run(ctx, executor.Options{Sudo: true}, "rm", "-f", path)
			if err != nil {
				return fmt.Errorf("removing %q: %w\nstderr: %s", path, err, res.Stderr)
			}
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing %q: %w", path, err)
		}
		return nil
	}
	out := []byte(next)
	if !strings.HasSuffix(next, "\n") {
		out = append(out, '\n')
	}
	perm := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		perm = info.Mode() & 0o777
	}
	if sudo {
		tmp, err := writeTempFile(out, perm)
		if err != nil {
			return fmt.Errorf("writing temp for %q: %w", path, err)
		}
		defer os.Remove(tmp)
		if err := sudoCopy(ctx, exec, tmp, path); err != nil {
			return fmt.Errorf("writing %q: %w", path, err)
		}
		return nil
	}
	return atomicWriteFile(path, out, perm)
}
