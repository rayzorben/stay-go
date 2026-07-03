// Package compose implements the "compose" resource: docker-compose projects
// brought up with `docker compose up -d`.
//
// File layout (Single Responsibility per file):
//
//	compose.go    — Resource struct, constructor, Type(), shared helpers
//	knowledge.go  — GatherKnowledge (is the project running?)
//	plan.go       — BuildPlan (desired-vs-actual diff via a content hash)
//	execute.go    — Execute (render the project, then up -d / down)
package compose

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/executor"
)

// Resource implements engine.Resource for docker-compose / podman compose projects.
type Resource struct {
	cfg         *config.Config
	exec        *executor.Executor
	nodeConfigs map[string]*config.ComposeEntry // nodeID → config entry; populated in BuildPlan
}

// New creates a compose Resource.
func New(cfg *config.Config, exec *executor.Executor) *Resource {
	return &Resource{
		cfg:         cfg,
		exec:        exec,
		nodeConfigs: make(map[string]*config.ComposeEntry),
	}
}

// Type implements engine.Resource.
func (r *Resource) Type() string { return "compose" }

// ─── Shared helpers ───────────────────────────────────────────────────────────

// resolveRuntime returns the container runtime CLI to use ("docker" or "podman").
// `<runtime> compose …` is the docker-compose-v2-compatible invocation for both.
func resolveRuntime(specified string) string {
	if specified != "" {
		return specified
	}
	for _, rt := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(rt); err == nil {
			return rt
		}
	}
	return "docker"
}

// standardComposeNames is docker compose's default file lookup order.
var standardComposeNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// composeFiles returns the compose file names (relative to dir) to pass via -f:
// the entry's explicit list, or the first standard name found plus its matching
// .override file when present (mirroring docker compose's own behaviour).
// An empty result is valid — compose then auto-discovers the file in dir.
func composeFiles(dir string, entry *config.ComposeEntry) []string {
	if len(entry.Files) > 0 {
		return entry.Files
	}
	for _, name := range standardComposeNames {
		if !regularFileExists(filepath.Join(dir, name)) {
			continue
		}
		files := []string{name}
		base := strings.TrimSuffix(name, filepath.Ext(name))
		for _, ext := range []string{".yaml", ".yml"} {
			if ov := base + ".override" + ext; regularFileExists(filepath.Join(dir, ov)) {
				files = append(files, ov)
			}
		}
		return files
	}
	return nil
}

// composeArgs returns the leading `compose …` arguments for a rendered project
// directory: project directory, project name, -f file(s) and (only when a
// non-default env file is configured) --env-file. Append the subcommand
// ("up", "-d", … or "down", …) to the result.
func composeArgs(dir string, entry *config.ComposeEntry) []string {
	args := []string{"compose", "--project-directory", dir, "-p", entry.ProjectName()}
	for _, f := range composeFiles(dir, entry) {
		args = append(args, "-f", filepath.Join(dir, f))
	}
	if entry.EnvFile != "" {
		args = append(args, "--env-file", filepath.Join(dir, entry.EnvFile))
	}
	return args
}

// projectLabelFilter is the `docker ps` filter that matches a compose project's
// containers (both docker compose and podman compose set this label).
func projectLabelFilter(project string) string {
	return "label=com.docker.compose.project=" + project
}

func regularFileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
