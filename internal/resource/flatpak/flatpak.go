// Package flatpak implements the "flatpak" resource.
//
// File layout (Single Responsibility per file):
//   flatpak.go   — Resource struct, constructor, Type(), node ID helpers
//   knowledge.go — GatherKnowledge (queries flatpak for installed remotes and apps)
//   plan.go      — BuildPlan (desired-vs-actual diff)
//   execute.go   — Execute (add/remove remotes and apps)
//
// The flatpak binary itself is managed as an implicit package dependency:
// New() injects "flatpak" into cfg.Packages when absent, so the packages resource
// installs it before any flatpak node runs.
//
// All remotes and apps are managed at the user level (--user), requiring no sudo.
package flatpak

import (
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/executor"
)

const (
	remotePrefix = "flatpak/remote/"
	appPrefix    = "flatpak/app/"
)

// Resource implements engine.Resource for Flatpak remote and app management.
type Resource struct {
	cfg  *config.Config
	exec *executor.Executor
}

// New creates a flatpak Resource. When flatpak remotes or apps are configured,
// it injects a "flatpak" package entry into cfg.Packages so that every flatpak
// node's implicit packages/flatpak dependency is satisfied by the packages resource.
// When no flatpak config exists (e.g. inside a distrobox), nothing is injected.
func New(cfg *config.Config, exec *executor.Executor) *Resource {
	hasFlatpakConfig := len(cfg.Flatpak.Remotes) > 0 || len(cfg.Flatpak.Apps) > 0
	if hasFlatpakConfig && !hasFlatpakPackage(cfg) {
		cfg.Packages = append(cfg.Packages, config.PackageEntry{Name: "flatpak", Level: "common"})
	}
	return &Resource{cfg: cfg, exec: exec}
}

// Type implements engine.Resource.
func (r *Resource) Type() string { return "flatpak" }

// hasFlatpakPackage reports whether flatpak is already in the packages list
// as a non-removal entry.
func hasFlatpakPackage(cfg *config.Config) bool {
	for _, p := range cfg.Packages {
		if p.Name == "flatpak" && !p.Remove {
			return true
		}
	}
	return false
}

func remoteNodeID(name string) string { return remotePrefix + name }
func appNodeID(appID string) string   { return appPrefix + appID }

func isRemoteNode(id string) bool {
	return strings.HasPrefix(id, remotePrefix) && len(id) > len(remotePrefix)
}

// splitLines splits s on newlines and returns trimmed, non-empty lines.
func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
