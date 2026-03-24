package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rayben/stay-go/internal/config"
)

func TestLoadPackages(t *testing.T) {
	raw := `
packages:
  - neovim
  - git
`
	cfg := loadString(t, raw)
	if len(cfg.Packages) != 2 {
		t.Fatalf("want 2 packages, got %d", len(cfg.Packages))
	}
	if cfg.Packages[0].Name != "neovim" {
		t.Errorf("want neovim, got %q", cfg.Packages[0].Name)
	}
}

func TestLoadPackageMapForm(t *testing.T) {
	raw := `
packages:
  - name: neovim
`
	cfg := loadString(t, raw)
	if len(cfg.Packages) != 1 || cfg.Packages[0].Name != "neovim" {
		t.Errorf("map-form package not parsed correctly: %+v", cfg.Packages)
	}
}

func TestLoadUsers(t *testing.T) {
	raw := `
users:
  - name: alice
    shell: /bin/zsh
`
	cfg := loadString(t, raw)
	if len(cfg.Users) != 1 {
		t.Fatalf("want 1 user, got %d", len(cfg.Users))
	}
	u := cfg.Users[0]
	if u.Name != "alice" || u.Shell != "/bin/zsh" {
		t.Errorf("unexpected user: %+v", u)
	}
}

func TestLoadServiceWithDepends(t *testing.T) {
	raw := `
services:
  - service: docker
    depends:
      - packages: [docker]
`
	cfg := loadString(t, raw)
	if len(cfg.Services) != 1 {
		t.Fatalf("want 1 service, got %d", len(cfg.Services))
	}
	s := cfg.Services[0]
	if s.Service != "docker" {
		t.Errorf("want docker, got %q", s.Service)
	}
	ids := s.DependsOnIDs()
	if len(ids) != 1 || ids[0] != "packages/docker" {
		t.Errorf("unexpected depends IDs: %v", ids)
	}
}

func TestServiceIsEnabledDefault(t *testing.T) {
	raw := `
services:
  - service: sshd
`
	cfg := loadString(t, raw)
	if !cfg.Services[0].IsEnabled() {
		t.Error("expected enabled=true by default")
	}
}

func TestHash_Deterministic(t *testing.T) {
	h1 := config.Hash("neovim")
	h2 := config.Hash("neovim")
	if h1 != h2 {
		t.Error("Hash is not deterministic")
	}
}

func TestHash_Distinct(t *testing.T) {
	if config.Hash("neovim") == config.Hash("git") {
		t.Error("different values produced the same hash")
	}
}

func TestNodeID(t *testing.T) {
	id := config.NodeID("packages", "neovim")
	if id != "packages/neovim" {
		t.Errorf("want packages/neovim, got %q", id)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func loadString(t *testing.T, yaml string) *config.Config {
	t.Helper()
	f := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(f, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
