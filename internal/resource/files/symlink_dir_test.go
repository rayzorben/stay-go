package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
)

func newState() *state.State { return &state.State{Nodes: map[string]state.NodeEntry{}} }

func planOne(t *testing.T, cfg *config.Config, knowledge map[string]bool, st *state.State) (*Resource, *engine.PlanNode) {
	t.Helper()
	return planWith(t, New(cfg, nil), knowledge, st)
}

func planWith(t *testing.T, r *Resource, knowledge map[string]bool, st *state.State) (*Resource, *engine.PlanNode) {
	t.Helper()
	nodes, err := r.BuildPlan(context.Background(), knowledge, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes: %d", len(nodes))
	}
	return r, nodes[0]
}

// A directory source with symlink: true plans an ADD, and its hash depends on
// the path only — files changing inside the directory must not cause UPDATE.
func TestBuildPlan_symlinkDirectoryHashesPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Code")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Files: []config.FileEntry{{Source: src, Target: filepath.Join(dir, "code"), Symlink: true}}}

	_, first := planOne(t, cfg, map[string]bool{}, newState())
	if first.Action != engine.ActionAdd {
		t.Fatalf("got %q (%s), want ADD", first.Action, first.SkipReason)
	}
	if err := os.WriteFile(filepath.Join(src, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, second := planOne(t, cfg, map[string]bool{}, newState())
	if first.ConfigHash != second.ConfigHash {
		t.Fatal("hash changed when a file inside the linked directory changed")
	}
}

// A directory source without symlink: true can't be copied — SKIP with a reason
// that says why, rather than "not found".
func TestBuildPlan_directoryWithoutSymlinkSkips(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Files: []config.FileEntry{{Source: dir, Target: filepath.Join(t.TempDir(), "x")}}}
	_, node := planOne(t, cfg, map[string]bool{}, newState())
	if node.Action != engine.ActionSkip {
		t.Fatalf("got %q, want SKIP", node.Action)
	}
}

// Execute links a directory, and re-linking after the source moved replaces
// the existing link instead of nesting a new one inside the old directory.
func TestExecute_symlinkDirectoryRelinks(t *testing.T) {
	dir := t.TempDir()
	oldSrc := filepath.Join(dir, "PC", "development")
	newSrc := filepath.Join(dir, "Ray", "Code")
	for _, d := range []string{oldSrc, newSrc} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(dir, "home", "code")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldSrc, target); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Files: []config.FileEntry{{Source: newSrc, Target: target, Symlink: true}}}
	r := New(cfg, nil)
	knowledge, err := r.GatherKnowledge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(knowledge) != 0 {
		t.Fatal("link to the old location should be reported absent")
	}
	st := newState()
	_, node := planWith(t, r, map[string]bool{}, st)
	if err := r.Execute(context.Background(), node, st); err != nil {
		t.Fatal(err)
	}
	if dest, err := os.Readlink(target); err != nil || dest != newSrc {
		t.Fatalf("link = %q (%v), want %q", dest, err, newSrc)
	}
	if _, err := os.Lstat(filepath.Join(oldSrc, "Code")); err == nil {
		t.Fatal("a link was created inside the old directory")
	}
}
