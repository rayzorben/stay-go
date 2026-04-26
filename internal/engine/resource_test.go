package engine

import (
	"testing"

	"github.com/rayzorben/stay-go/internal/state"
)

func TestDetermineAction_Add(t *testing.T) {
	st, _ := state.Load("/nonexistent")
	action := DetermineAction("packages/neovim", false, "h1", st)
	if action != ActionAdd {
		t.Errorf("want ADD, got %v", action)
	}
}

func TestDetermineAction_Adopt_NotInState(t *testing.T) {
	st, _ := state.Load("/nonexistent")
	// In knowledge, not in state → ADOPT (first-time tracking, shown once)
	action := DetermineAction("packages/git", true, "h1", st)
	if action != ActionAdopt {
		t.Errorf("want ADOPT, got %v", action)
	}
}

func TestDetermineAction_Track_InState(t *testing.T) {
	st, _ := state.Load("/nonexistent")
	st.Set("packages/git", "h1", "", nil)
	action := DetermineAction("packages/git", true, "h1", st)
	if action != ActionTrack {
		t.Errorf("want TRACK, got %v", action)
	}
}

func TestDetermineAction_Update(t *testing.T) {
	st, _ := state.Load("/nonexistent")
	st.Set("users/alice", "old-hash", "", nil)
	action := DetermineAction("users/alice", true, "new-hash", st)
	if action != ActionUpdate {
		t.Errorf("want UPDATE, got %v", action)
	}
}

func TestStateRemovals_Basic(t *testing.T) {
	st, _ := state.Load("/nonexistent")
	st.Set("packages/neovim", "h1", "", nil)
	st.Set("packages/vim", "h2", "", nil)
	st.Set("users/alice", "h3", "", nil) // different resource type — should be ignored

	configSet := map[string]bool{"neovim": true} // vim not in config
	nodes := StateRemovals("packages", configSet, nil, st)

	if len(nodes) != 1 {
		t.Fatalf("want 1 removal, got %d", len(nodes))
	}
	if nodes[0].ID != "packages/vim" || nodes[0].Action != ActionRemove {
		t.Errorf("unexpected removal node: %+v", nodes[0])
	}
}

func TestStateRemovals_Empty(t *testing.T) {
	st, _ := state.Load("/nonexistent")
	nodes := StateRemovals("packages", map[string]bool{"neovim": true}, nil, st)
	if len(nodes) != 0 {
		t.Errorf("expected no removals, got %d", len(nodes))
	}
}
