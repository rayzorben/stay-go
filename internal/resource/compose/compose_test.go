package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayzorben/stay-go/internal/config"
)

func TestProjectNameDefaultsToPathBasename(t *testing.T) {
	e := &config.ComposeEntry{Path: "/abs/config/files/docker/immich"}
	if got := e.ProjectName(); got != "immich" {
		t.Fatalf("ProjectName() = %q, want immich", got)
	}
	e.Project = "custom"
	if got := e.ProjectName(); got != "custom" {
		t.Fatalf("ProjectName() = %q, want custom", got)
	}
}

func TestComposeFilesDetectsStandardNameAndOverride(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "docker-compose.yml"), "services: {}\n")
	write(t, filepath.Join(dir, "docker-compose.override.yml"), "services: {}\n")
	write(t, filepath.Join(dir, "notes.txt"), "ignored\n")

	got := composeFiles(dir, &config.ComposeEntry{})
	want := []string{"docker-compose.yml", "docker-compose.override.yml"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("composeFiles = %v, want %v", got, want)
	}

	// Explicit list wins.
	got = composeFiles(dir, &config.ComposeEntry{Files: []string{"a.yml", "b.yml"}})
	if strings.Join(got, ",") != "a.yml,b.yml" {
		t.Fatalf("composeFiles (explicit) = %v", got)
	}
}

func TestRenderProjectSubstitutesVarsAndSecrets(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	src := t.TempDir()
	write(t, filepath.Join(src, "docker-compose.yml"),
		"services:\n  db:\n    image: postgres\n    volumes:\n      - ${data_dir}:/data\n")
	write(t, filepath.Join(src, ".env"), "DB_PASSWORD=${secrets.immich.db_password}\nKEEP=${IMMICH_VERSION:-release}\n")
	write(t, filepath.Join(src, "sub", "extra.yml"), "x: ${data_dir}\n")

	cfg := &config.Config{
		Vars:             map[string]string{"data_dir": "/storage/immich"},
		DecryptedSecrets: map[string]string{"immich.db_password": "s3cret"},
	}
	entry := &config.ComposeEntry{Path: src} // project "immich" (basename of a TempDir is random; set explicitly)
	entry.Project = "immich"

	dir, err := renderProject(entry, cfg)
	if err != nil {
		t.Fatalf("renderProject: %v", err)
	}

	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if got := string(env); !strings.Contains(got, "DB_PASSWORD=s3cret") || !strings.Contains(got, "KEEP=${IMMICH_VERSION:-release}") {
		t.Fatalf(".env not rendered as expected:\n%s", got)
	}
	compose, _ := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if !strings.Contains(string(compose), "/storage/immich:/data") {
		t.Fatalf("compose file var not substituted:\n%s", compose)
	}
	extra, _ := os.ReadFile(filepath.Join(dir, "sub", "extra.yml"))
	if strings.TrimSpace(string(extra)) != "x: /storage/immich" {
		t.Fatalf("nested file not rendered: %q", extra)
	}

	// Rendered files must be owner-only.
	info, _ := os.Stat(filepath.Join(dir, ".env"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode = %v, want 0600", info.Mode().Perm())
	}

	// A second render clears stale files.
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("X=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(src, "sub", "extra.yml"))
	dir, err = renderProject(entry, cfg)
	if err != nil {
		t.Fatalf("renderProject (2): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "extra.yml")); !os.IsNotExist(err) {
		t.Fatalf("stale file survived re-render: %v", err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
