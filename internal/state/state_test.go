package state_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rayben/stay-go/internal/state"
)

func TestLoadMissingFile(t *testing.T) {
	s, err := state.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("expected no error for missing state file, got: %v", err)
	}
	if s.Nodes == nil {
		t.Error("Nodes map should be initialised")
	}
}

func TestSetGetDelete(t *testing.T) {
	s, _ := state.Load(filepath.Join(t.TempDir(), "state.json"))

	s.Set("packages/neovim", "abc123", "", nil)
	e, ok := s.Get("packages/neovim")
	if !ok {
		t.Fatal("expected node to be present")
	}
	if e.Hash != "abc123" {
		t.Errorf("want hash abc123, got %q", e.Hash)
	}

	s.Delete("packages/neovim")
	if s.Has("packages/neovim") {
		t.Error("expected node to be deleted")
	}
}

func TestSaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := state.Load(path)
	s.Set("packages/git", "hashval", "", nil)

	if err := state.Save(s, path); err != nil {
		t.Fatal(err)
	}

	s2, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := s2.Get("packages/git")
	if !ok || e.Hash != "hashval" {
		t.Errorf("reloaded state does not match: %+v", e)
	}
}

func TestAtomicSave_NoCorruptionOnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	// Pre-create with some content.
	os.WriteFile(path, []byte(`{"nodes":{}}`), 0o600)

	s, _ := state.Load(path)
	s.Set("users/alice", "h1", "", nil)
	state.Save(s, path)

	// Tmp file should not be left behind.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temporary file was not cleaned up")
	}
}
