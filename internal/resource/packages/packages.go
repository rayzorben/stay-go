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

// syncNodePrefix is the ID prefix for package index-sync plan nodes
// (e.g. "packages/__sync__/nodeps" or "packages/__sync__/<hash>").
// Packages that share the same set of dependencies share a single sync node so
// that apt-get update (or equivalent) only runs once per dependency group.
const syncNodePrefix = "packages/__sync__/"

// packageSyncGroupID returns a stable sync node ID for the given dependency-set
// key. depsKey is the sorted, null-separated concatenation of all dep IDs that
// the group of packages shares. An empty key means "no dependencies".
func packageSyncGroupID(depsKey string) string {
	if depsKey == "" {
		return syncNodePrefix + "nodeps"
	}
	return syncNodePrefix + config.Hash(depsKey)
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

