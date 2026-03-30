// Package distrobox implements the "distrobox" resource.
//
// File layout (Single Responsibility per file):
//
//	distrobox.go — Resource struct, constructor, Type(), shared helpers,
//	               binary injection, sub-config writing
//	knowledge.go  — GatherKnowledge (distrobox list)
//	plan.go       — BuildPlan (host-level box + in-box apply nodes)
//	execute.go    — Execute (create/remove box, run guest stay-go)
//
// # Architecture
//
// Each distrobox entry produces up to two plan nodes:
//
//	"distrobox/<name>"        — host-level: create / remove the container
//	"distrobox/<name>/apply"  — in-box: run stay-go inside to apply packages
//	                            and commands declared under the entry
//
// The host is the sole controller of all state. In-box state is stored in
// a per-box state file at:
//
//	~/.local/share/stay-go/distrobox/<name>/state.json
//
// The sub-config (packages + commands for the box) is written as:
//
//	~/.local/share/stay-go/distrobox/<name>/default.yaml
//
// Since distrobox shares the host home directory with the container, both
// paths are accessible identically from inside and outside the box.
//
// The host injects the current stay-go binary to:
//
//	~/.local/bin/stay-go
//
// and then invokes it inside the box using:
//
//	distrobox enter -n <name> -- ~/.local/bin/stay-go --yes -c <configDir> --state <statePath>
package distrobox

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/executor"
	"gopkg.in/yaml.v3"
)

// Resource implements engine.Resource for distrobox container management.
type Resource struct {
	cfg         *config.Config
	exec        *executor.Executor
	nodeConfigs map[string]*config.DistroboxEntry // nodeID → config entry; populated in BuildPlan
	stateDir    string                            // base dir: ~/.local/share/stay-go/distrobox
}

// New creates a distrobox Resource.
func New(cfg *config.Config, exec *executor.Executor) *Resource {
	home, _ := os.UserHomeDir()
	return &Resource{
		cfg:         cfg,
		exec:        exec,
		nodeConfigs: make(map[string]*config.DistroboxEntry),
		stateDir:    filepath.Join(home, ".local", "share", "stay-go", "distrobox"),
	}
}

// Type implements engine.Resource.
func (r *Resource) Type() string { return "distrobox" }

// ─── Node ID helpers ──────────────────────────────────────────────────────────

// boxNodeID returns the canonical host-level node ID for a distrobox container.
func boxNodeID(name string) string { return "distrobox/" + name }

// applyNodeID returns the canonical node ID for the in-box apply operation.
func applyNodeID(name string) string { return "distrobox/" + name + "/apply" }

// isApplyNode reports whether the node ID represents an in-box apply node.
func isApplyNode(id string) bool {
	// Format: distrobox/<name>/apply — exactly two slashes total.
	count := 0
	for _, c := range id {
		if c == '/' {
			count++
		}
	}
	return count == 2
}

// ─── Path helpers ─────────────────────────────────────────────────────────────

// boxStateDir returns the directory holding per-box state and sub-config.
func (r *Resource) boxStateDir(name string) string {
	return filepath.Join(r.stateDir, name)
}

// boxStatePath returns the path to the per-box state JSON file.
func (r *Resource) boxStatePath(name string) string {
	return filepath.Join(r.stateDir, name, "state.json")
}

// boxConfigDir returns the config directory for the in-box stay-go invocation.
// LoadAll expects a directory; the sub-config is written as default.yaml inside.
func (r *Resource) boxConfigDir(name string) string {
	return filepath.Join(r.stateDir, name)
}

// ─── Hashing ─────────────────────────────────────────────────────────────────

// distroboxHash returns a deterministic hash of the host-level container config.
// Only fields that affect the box creation command are included.
func distroboxHash(e *config.DistroboxEntry) string {
	return config.Hash(struct {
		Name     string
		Image    string
		Init     bool
		Home     string
		HomeSudo bool
	}{e.Name, e.Image, e.Init, e.Home, e.HomeSudo})
}

// inBoxHash returns a deterministic hash of the in-box resources.
// Any change to packages, commands, or exports triggers re-application.
func inBoxHash(e *config.DistroboxEntry) string {
	return config.Hash(struct {
		Packages []config.PackageEntry
		Commands []config.CommandEntry
		Exports  []string
	}{e.Packages, e.Commands, e.Exports})
}

// ─── Binary injection ────────────────────────────────────────────────────────

// ensureGuestBinary always copies the running stay-go binary (os.Executable) to
// ~/.local/bin/stay-go via a temp file + rename. We intentionally do not skip
// when size/mtime match: different builds can share size and timestamps, and
// EvalSymlinks can make the source path equal the target while the repo binary
// is actually newer. Since distrobox shares the host home, the copy is visible
// inside the box. Returns the absolute path to the guest binary.
//
// Plan/execute and --guest-knowledge always invoke this path after ensureGuestBinary.
// When testing manually inside a box, do not run bare "stay-go" — PATH may resolve
// to an older distro package (e.g. /usr/bin/stay-go) that is not this copy.
// Use the returned path explicitly, e.g. distrobox enter -n NAME -- /home/you/.local/bin/stay-go --version
func (r *Resource) ensureGuestBinary() (string, error) {
	binPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolving current binary: %w", err)
	}
	binPath, err = filepath.EvalSymlinks(binPath)
	if err != nil {
		return "", fmt.Errorf("resolving binary symlinks: %w", err)
	}

	home, _ := os.UserHomeDir()
	targetDir := filepath.Join(home, ".local", "bin")
	targetBin := filepath.Join(targetDir, "stay-go")

	srcInfo, err := os.Stat(binPath)
	if err != nil {
		return "", fmt.Errorf("stat source binary: %w", err)
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", targetDir, err)
	}

	src, err := os.Open(binPath)
	if err != nil {
		return "", fmt.Errorf("opening source binary: %w", err)
	}
	defer src.Close()

	tmpBin := targetBin + ".tmp"
	dst, err := os.OpenFile(tmpBin, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return "", fmt.Errorf("creating guest binary: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(tmpBin)
		return "", fmt.Errorf("copying binary: %w", err)
	}
	dst.Close()

	// Preserve the source mtime (cosmetic; we no longer skip copy on mtime).
	_ = os.Chtimes(tmpBin, srcInfo.ModTime(), srcInfo.ModTime())

	if err := os.Rename(tmpBin, targetBin); err != nil {
		os.Remove(tmpBin)
		return "", fmt.Errorf("installing guest binary: %w", err)
	}
	return targetBin, nil
}

// ─── Sub-config writing ───────────────────────────────────────────────────────

// guestConfig is the minimal YAML written for an in-box stay-go invocation.
// It contains only the resource types that stay-go manages inside a distrobox.
type guestConfig struct {
	Packages []config.PackageEntry `yaml:"packages,omitempty"`
	Commands []config.CommandEntry `yaml:"commands,omitempty"`
}

// writeBoxConfig serialises the in-box packages and commands to
// <stateDir>/<name>/default.yaml so that LoadAll can load it as a config dir.
func (r *Resource) writeBoxConfig(entry *config.DistroboxEntry) error {
	dir := r.boxStateDir(entry.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating box state dir: %w", err)
	}

	cfg := guestConfig{
		Packages: entry.Packages,
		Commands: entry.Commands,
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling box config: %w", err)
	}

	target := filepath.Join(dir, "default.yaml")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing box config: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("committing box config: %w", err)
	}
	return nil
}

// ─── distrobox create args ────────────────────────────────────────────────────

// needsHomeSudo reports whether creating this entry requires sudo — only when
// home_sudo is set and Home is an absolute path outside the user's home dir.
func needsHomeSudo(e *config.DistroboxEntry) bool {
	if !e.HomeSudo || e.Home == "" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return true // conservative
	}
	// Home is already ~ -expanded by vars, so a simple prefix check is enough.
	return !strings.HasPrefix(e.Home, home)
}

// buildCreateArgs constructs the argument slice for `distrobox create`.
func buildCreateArgs(e *config.DistroboxEntry) []string {
	args := []string{"create", "--yes", "--name", e.Name, "--image", e.Image}
	if e.Init {
		args = append(args, "--init")
	}
	if e.Home != "" {
		args = append(args, "--home", e.Home)
	}
	return args
}
