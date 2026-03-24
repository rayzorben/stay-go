// Package users implements the "users" resource.
//
// File layout (Single Responsibility per file):
//   users.go     — Resource struct, constructor, Type(), passwd helpers
//   knowledge.go — GatherKnowledge (reads /etc/passwd natively)
//   plan.go      — BuildPlan (desired-vs-actual diff)
//   execute.go   — Execute (useradd / usermod / userdel)
package users

import (
	"os"
	"strings"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/executor"
)

// Resource implements engine.Resource for system user management.
type Resource struct {
	cfg         *config.Config
	exec        *executor.Executor
	nodeConfigs map[string]*config.UserEntry // nodeID → config entry; populated in BuildPlan
}

// New creates a users Resource.
func New(cfg *config.Config, exec *executor.Executor) *Resource {
	return &Resource{
		cfg:         cfg,
		exec:        exec,
		nodeConfigs: make(map[string]*config.UserEntry),
	}
}

// Type implements engine.Resource.
func (r *Resource) Type() string { return "users" }

// ─── System user model ────────────────────────────────────────────────────────

// systemUser holds the live state for a user parsed from /etc/passwd.
type systemUser struct {
	Name    string
	UID     string
	GID     string
	GECOS   string // full name / comment field
	HomeDir string
	Shell   string
}

// parseGroupMembers reads /etc/group and returns a map of groupname → set of member usernames.
func parseGroupMembers() (map[string]map[string]bool, error) {
	data, err := os.ReadFile("/etc/group")
	if err != nil {
		return nil, err
	}
	result := make(map[string]map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			continue
		}
		members := make(map[string]bool)
		if fields[3] != "" {
			for _, m := range strings.Split(fields[3], ",") {
				members[m] = true
			}
		}
		result[fields[0]] = members
	}
	return result, nil
}

// parsePasswd reads /etc/passwd and returns a map of username → systemUser.
// Using /etc/passwd directly is the preferred native approach on Linux,
// capturing all users including those not in NSS (e.g. during chroot/container).
func parsePasswd() (map[string]systemUser, error) {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil, err
	}
	users := make(map[string]systemUser)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		users[fields[0]] = systemUser{
			Name:    fields[0],
			UID:     fields[2],
			GID:     fields[3],
			GECOS:   fields[4],
			HomeDir: fields[5],
			Shell:   fields[6],
		}
	}
	return users, nil
}

// userHash returns a deterministic hash of the user config fields that stay-go manages.
func userHash(e *config.UserEntry) string {
	return config.Hash(struct {
		Username string
		Name     string
		Shell    string
		Home     string
		UID      string
		Groups   []string
	}{e.Username, e.Name, e.Shell, e.Home, e.UID, e.Groups})
}

// sysUserMatchesConfig returns true when the live system user already reflects
// the desired config state (so no modification is needed).
// sysGroupMembers maps groupname → member set. managedGroups is the full list
// of groups managed by stay-go, used to detect memberships that should be removed.
func sysUserMatchesConfig(su systemUser, e *config.UserEntry, sysGroupMembers map[string]map[string]bool, managedGroups []string) bool {
	if e.Name != "" && su.GECOS != e.Name {
		return false
	}
	if e.Shell != "" && su.Shell != e.Shell {
		return false
	}
	if e.Home != "" && su.HomeDir != e.Home {
		return false
	}
	if e.UID != "" && su.UID != e.UID {
		return false
	}
	desiredGroupSet := make(map[string]bool, len(e.Groups))
	for _, g := range e.Groups {
		desiredGroupSet[g] = true
	}
	// User must be a member of all desired groups.
	for _, g := range e.Groups {
		if members, ok := sysGroupMembers[g]; !ok || !members[su.Name] {
			return false
		}
	}
	// User must not be a member of managed groups not in the desired set.
	for _, g := range managedGroups {
		if !desiredGroupSet[g] {
			if members, ok := sysGroupMembers[g]; ok && members[su.Name] {
				return false
			}
		}
	}
	return true
}
