package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression test: mergeConfigs must carry the Flatpak section through a layer
// merge. It was previously omitted, so any config using !include silently
// dropped every flatpak remote and app from the plan.
func TestMergeConfigsPreservesFlatpak(t *testing.T) {
	base := &Config{
		Flatpak: FlatpakConfig{
			Remotes: []FlatpakRemoteEntry{{Name: "flathub", URL: "https://base.example/repo"}},
			Apps:    []FlatpakAppEntry{{AppID: "org.example.Base"}},
		},
	}
	override := &Config{
		Flatpak: FlatpakConfig{
			Remotes: []FlatpakRemoteEntry{{Name: "flathub", URL: "https://override.example/repo"}},
			Apps:    []FlatpakAppEntry{{AppID: "com.usebottles.bottles"}},
		},
	}

	merged := mergeConfigs(base, override)

	if len(merged.Flatpak.Remotes) != 1 {
		t.Fatalf("expected 1 merged remote, got %d", len(merged.Flatpak.Remotes))
	}
	if got := merged.Flatpak.Remotes[0].URL; got != "https://override.example/repo" {
		t.Errorf("override remote should win by name: got URL %q", got)
	}
	if len(merged.Flatpak.Apps) != 2 {
		t.Fatalf("expected 2 merged apps, got %d", len(merged.Flatpak.Apps))
	}
	apps := map[string]bool{}
	for _, a := range merged.Flatpak.Apps {
		apps[a.AppID] = true
	}
	if !apps["org.example.Base"] || !apps["com.usebottles.bottles"] {
		t.Errorf("merged apps missing entries: %v", apps)
	}
}

// End-to-end regression: a flatpak section declared in an included user layer
// must survive LoadAll.
func TestLoadAllPreservesFlatpakFromIncludedLayer(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "users"), 0o755); err != nil {
		t.Fatal(err)
	}

	defaultYAML := `
packages:
  - git
user_config: !include "${config_root}/users/layer.yaml"
`
	layerYAML := `
flatpak:
  remotes:
    - name: flathub
      url: https://dl.flathub.org/repo/flathub.flatpakrepo
  apps:
    - app: com.usebottles.bottles
`
	if err := os.WriteFile(filepath.Join(dir, "default.yaml"), []byte(defaultYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "users", "layer.yaml"), []byte(layerYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	if len(cfg.Flatpak.Remotes) != 1 || cfg.Flatpak.Remotes[0].Name != "flathub" {
		t.Errorf("flatpak remotes lost in layer merge: %+v", cfg.Flatpak.Remotes)
	}
	if len(cfg.Flatpak.Apps) != 1 || cfg.Flatpak.Apps[0].AppID != "com.usebottles.bottles" {
		t.Errorf("flatpak apps lost in layer merge: %+v", cfg.Flatpak.Apps)
	}
	if got := cfg.Flatpak.Apps[0].SourceFile; got != "/users/layer.yaml" {
		t.Errorf("flatpak app SourceFile = %q, want /users/layer.yaml", got)
	}
}
