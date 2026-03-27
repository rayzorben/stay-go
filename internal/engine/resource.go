// Package engine defines the contracts (interfaces, types, shared helpers)
// that all resource implementations depend on.
//
// SOLID design:
//   S — Each resource type has exactly one responsibility (managing one resource kind).
//   O — New resource types extend the system without modifying existing code.
//   L — All Resource implementations are substitutable in the engine.
//   I — The Resource interface is composed of three segregated sub-interfaces.
//   D — The engine depends on Resource abstractions, not concrete packages.
package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rayben/stay-go/internal/state"
)

// ErrExecutionFailed is returned by Engine.Run when one or more plan nodes
// failed during execution. Failures have already been reported to stdout;
// callers should exit non-zero without printing additional output.
var ErrExecutionFailed = errors.New("one or more nodes failed during execution")

// ─── Action types ─────────────────────────────────────────────────────────────

// ActionType describes what the engine will do for a single managed node.
type ActionType string

const (
	// ActionTrack — node is in state with a matching config hash.
	// Completely silent: state is synced, nothing shown in the plan,
	// no confirmation required. This is the steady-state for a managed node.
	ActionTrack ActionType = "TRACK"

	// ActionAdopt — node exists on the system but has never been tracked by
	// stay-go. It will be added to state without any system modification.
	// Shown in the plan once; on subsequent runs it becomes ActionTrack.
	ActionAdopt ActionType = "ADOPT"

	// ActionAdd — node is in config but not on the system. Will be created.
	ActionAdd ActionType = "ADD"

	// ActionUpdate — node is on the system and in state, but the config hash
	// has changed since it was last applied. Will be modified.
	ActionUpdate ActionType = "UPDATE"

	// ActionRemove — node was previously tracked but is no longer in config.
	// Will be deleted from the system and removed from state.
	ActionRemove ActionType = "REMOVE"

	// ActionLevel — the config hash is unchanged but the entry has moved to a
	// different config layer (e.g. common → user:rayben). No system commands
	// are run; only the stored level in state is updated.
	ActionLevel ActionType = "LEVEL"

	// ActionSkip — a dependency is not met or failed. No execution attempted.
	ActionSkip ActionType = "SKIP"
)

// ─── Plan node ────────────────────────────────────────────────────────────────

// PlanNode represents a single item in the execution plan.
type PlanNode struct {
	// ID is the canonical node identifier, e.g. "packages/neovim".
	ID string

	// ResourceType matches Resource.Type(), e.g. "packages".
	ResourceType string

	// DisplayName is the short human-readable label shown in the plan output.
	DisplayName string

	// Action is the planned action for this node.
	Action ActionType

	// SkipReason is populated when Action == ActionSkip.
	SkipReason string

	// ConfigHash is the deterministic hash of the node's config entry.
	// Stored in state after a successful TRACK, ADD, or UPDATE.
	ConfigHash string

	// DependsOn holds node IDs that must be successfully resolved before
	// this node can execute.
	DependsOn []string

	// Level describes the scope of this node. Conventional values:
	//   "common"      — system-wide (packages, system services, system users)
	//   "user:<name>" — user-scoped (user services, user-owned files)
	Level string

	// NeedsSudo indicates that executing this node requires sudo.
	// Used by the engine to pre-authenticate sudo once before the execute loop.
	NeedsSudo bool

	// Description is an optional plain-English explanation of what will change.
	// Resources populate this for UPDATE nodes (e.g. "shell /bin/fish → /bin/zsh")
	// and may populate it for ADD/REMOVE to add contextual detail.
	// Empty descriptions are silently omitted from the plan display.
	Description string

	// StateData holds the config values to persist alongside the hash.
	// Stored on every TRACK/ADOPT/ADD/UPDATE so that future runs can diff
	// new config against what was last applied rather than the live system.
	StateData map[string]interface{}

	// Notes are optional continuation lines rendered beneath the plan row with a
	// leading ↳ symbol, regardless of action type. Used by resources that need
	// to convey multi-line detail about a planned change (e.g. distrobox lists
	// the packages, commands, and exports that will be applied in-box).
	Notes []string

	// ExecutionErr is set by the engine if Execute returns an error.
	ExecutionErr error
}

// KnowledgeEntry describes a single item currently present on the live system.
type KnowledgeEntry struct {
	// ID is the canonical node ID, e.g. "packages/neovim".
	ID string
}

// ─── Interface segregation ────────────────────────────────────────────────────

// Knowledger queries the live system to discover what is currently present.
// Implementations must be safe to call concurrently with other Knowledgers.
type Knowledger interface {
	GatherKnowledge(ctx context.Context) ([]KnowledgeEntry, error)
}

// Planner builds the set of planned actions for a resource type given the
// combined knowledge (nodeID → present) and the current persisted state.
// It must also emit ActionRemove nodes for state entries no longer in config.
type Planner interface {
	BuildPlan(ctx context.Context, knowledge map[string]bool, st *state.State) ([]*PlanNode, error)
}

// NodeExecutor carries out a single node's action (ADD, UPDATE, REMOVE).
// It is the executor's responsibility to update state on success:
//
//	ADD/UPDATE → st.Set(node.ID, node.ConfigHash)
//	REMOVE     → st.Delete(node.ID)
//
// TRACK and SKIP are never passed to Execute.
type NodeExecutor interface {
	Execute(ctx context.Context, node *PlanNode, st *state.State) error
}

// Resource composes the three segregated interfaces into the complete contract
// that the engine requires of every resource type.
type Resource interface {
	// Type returns the resource type string matching the YAML key
	// ("packages", "users", "services", …).
	Type() string
	Knowledger
	Planner
	NodeExecutor
}

// ─── Shared helpers (DRY) ────────────────────────────────────────────────────

// DetermineAction returns the appropriate ActionType for a node given whether
// it is currently present on the system, its desired config hash, and the
// current persisted state.
//
// Callers may override the result for resource-specific cases (e.g. the users
// resource upgrades ActionAdopt → ActionUpdate when the live system state does
// not match the desired config).
func DetermineAction(id string, inKnowledge bool, configHash string, st *state.State) ActionType {
	stateEntry, inState := st.Get(id)
	switch {
	case !inKnowledge:
		// Not on the system at all — needs installation.
		return ActionAdd
	case inState && stateEntry.Hash == configHash:
		// Already managed and config unchanged — silent steady-state.
		return ActionTrack
	case inState:
		// Managed, but config hash changed since last apply — needs modification.
		return ActionUpdate
	default:
		// Present on system but never tracked by stay-go — adopt into management.
		// Shown in the plan once; becomes ActionTrack on all subsequent runs.
		return ActionAdopt
	}
}

// CheckLevelChange upgrades a TRACK action to ActionLevel when the node's
// stored level differs from the desired config level. Returns the (possibly
// updated) action and a human-readable description of the level change.
// No-ops if the action is not TRACK, the node is not yet in state, or the
// stored level is empty (pre-level-tracking state entry).
func CheckLevelChange(id, newLevel string, action ActionType, st *state.State) (ActionType, string) {
	if action != ActionTrack {
		return action, ""
	}
	entry, ok := st.Get(id)
	if !ok || entry.Level == "" || entry.Level == newLevel {
		return action, ""
	}
	return ActionLevel, fmt.Sprintf("level %s → %s", entry.Level, newLevel)
}

// StateRemovals returns ActionRemove PlanNodes for every node tracked in state
// under resourceType that is no longer present in configSet (name → true).
// This is shared by all resources to avoid duplicating the removal detection loop.
func StateRemovals(resourceType string, configSet map[string]bool, st *state.State) []*PlanNode {
	prefix := resourceType + "/"
	var nodes []*PlanNode
	for id := range st.Nodes {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		name := strings.TrimPrefix(id, prefix)
		if !configSet[name] {
			nodes = append(nodes, &PlanNode{
				ID:           id,
				ResourceType: resourceType,
				DisplayName:  name,
				Action:       ActionRemove,
			})
		}
	}
	return nodes
}
