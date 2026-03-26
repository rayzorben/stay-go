// Package files implements the "files" resource.
package files

import (
	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/executor"
)

// Resource manages file placements: secrets, local copies/symlinks,
// git clones, and HTTP downloads.
type Resource struct {
	cfg         *config.Config
	exec        *executor.Executor
	nodeConfigs map[string]*config.FileEntry
}

// New creates a new files Resource.
func New(cfg *config.Config, exec *executor.Executor) *Resource {
	return &Resource{cfg: cfg, exec: exec, nodeConfigs: make(map[string]*config.FileEntry)}
}

// Type returns the resource type identifier used in node IDs and plan display.
func (r *Resource) Type() string { return "files" }

// nodeID returns the canonical node ID for a file target path.
func nodeID(target string) string { return "files/" + target }
