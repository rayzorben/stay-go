// Package packages implements the "packages" resource.
//
// File layout (Single Responsibility per file):
//   packages.go  — Resource struct, constructor, Type()
//   knowledge.go — GatherKnowledge (system query)
//   plan.go      — BuildPlan (desired-vs-actual diff)
//   execute.go   — Execute (side-effectful changes)
//   managers.go  — PackageManager table and detection
package packages

import (
	"context"
	"fmt"
	"strings"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/executor"
)

// Resource implements engine.Resource for package management.
// Package manager detection is deferred until first actual use.
type Resource struct {
	cfg     *config.Config
	exec    *executor.Executor
	manager *PackageManager // set on first call to ensureManager
}

// syncNodePrefix is the ID prefix for per-package index-sync plan nodes
// ("packages/__sync__/<package name>"). Each install package gets its own sync
// so apt-like updates run after that package's depends (e.g. a repo command)
// and independent packages are not ordered after unrelated commands.
const syncNodePrefix = "packages/__sync__/"

func packageSyncID(packageName string) string {
	return syncNodePrefix + packageName
}

func isPackageSyncNode(id string) bool {
	return strings.HasPrefix(id, syncNodePrefix) && len(id) > len(syncNodePrefix)
}

// New creates a packages Resource.
func New(cfg *config.Config, exec *executor.Executor) *Resource {
	return &Resource{cfg: cfg, exec: exec}
}

// Type implements engine.Resource.
func (r *Resource) Type() string { return "packages" }

// ensureManager detects the available package manager on first use.
func (r *Resource) ensureManager(ctx context.Context) error {
	if r.manager != nil {
		return nil
	}
	m, err := Detect(ctx)
	if err != nil {
		return fmt.Errorf("detecting package manager: %w", err)
	}
	r.manager = m
	return nil
}

