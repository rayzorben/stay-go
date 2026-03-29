// Package groups implements the "groups" resource.
//
// File layout (Single Responsibility per file):
//
//	groups.go    — Resource struct, constructor, Type(), /etc/group helpers
//	knowledge.go — GatherKnowledge (reads /etc/group natively)
//	plan.go      — BuildPlan (desired-vs-actual diff)
//	execute.go   — Execute (groupadd / groupdel)
package groups

import (
	"os"
	"strings"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/executor"
)

// Resource implements engine.Resource for system group management.
type Resource struct {
	cfg  *config.Config
	exec *executor.Executor
}

// New creates a groups Resource.
func New(cfg *config.Config, exec *executor.Executor) *Resource {
	return &Resource{cfg: cfg, exec: exec}
}

// Type implements engine.Resource.
func (r *Resource) Type() string { return "groups" }

// ─── System group model ───────────────────────────────────────────────────────

// systemGroup holds the live state for a group parsed from /etc/group.
type systemGroup struct {
	Name    string
	GID     string
	Members []string
}

// parseGroup reads /etc/group and returns a map of groupname → systemGroup.
func parseGroup() (map[string]systemGroup, error) {
	data, err := os.ReadFile("/etc/group")
	if err != nil {
		return nil, err
	}
	groups := make(map[string]systemGroup)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			continue
		}
		var members []string
		if fields[3] != "" {
			members = strings.Split(fields[3], ",")
		}
		groups[fields[0]] = systemGroup{
			Name:    fields[0],
			GID:     fields[2],
			Members: members,
		}
	}
	return groups, nil
}
