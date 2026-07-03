package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rayzorben/stay-go/internal/config"
)

// A symlink that resolves to the intended source is "present" (TRACK).
func TestGatherKnowledge_symlinkCorrectTargetIsPresent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.yaml")
	if err := os.WriteFile(src, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(src, target); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Files: []config.FileEntry{{Source: src, Target: target, Symlink: true}}}
	r := New(cfg, nil)
	entries, err := r.GatherKnowledge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected symlink pointing to source to be present, got %d entries", len(entries))
	}
}

// A symlink pointing at a stale location (e.g. the repo moved and ${config_root}
// now resolves elsewhere) must be reported ABSENT so the planner re-links it,
// even though the old link still exists on disk (dangling). Regression for the
// "moved ~/stay-go → ~/Dropbox/... left broken symlinks untouched" bug.
func TestGatherKnowledge_symlinkStaleTargetIsAbsent(t *testing.T) {
	dir := t.TempDir()
	newSrc := filepath.Join(dir, "new", "src.yaml")
	if err := os.MkdirAll(filepath.Dir(newSrc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newSrc, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Link still points at the old (now non-existent) path — dangling.
	oldSrc := filepath.Join(dir, "old", "src.yaml")
	target := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(oldSrc, target); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Files: []config.FileEntry{{Source: newSrc, Target: target, Symlink: true}}}
	r := New(cfg, nil)
	entries, err := r.GatherKnowledge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected stale symlink to be reported absent (re-link), got %d entries", len(entries))
	}
}
