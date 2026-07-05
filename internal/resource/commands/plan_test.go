package commands

import (
	"context"
	"testing"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
)

func TestCommandEntryDependsOnSkipsFolders(t *testing.T) {
	e := &config.CommandEntry{
		Name: "c",
		Depends: []map[string][]string{
			{"packages": {"git"}},
			{"folders": {"/tmp/foo"}},
		},
	}
	ids := e.DependsOnIDs()
	if len(ids) != 1 || ids[0] != "packages/git" {
		t.Fatalf("DependsOnIDs = %v, want [packages/git]", ids)
	}
}

// A command removed from config must produce a REMOVE node that is NOT marked
// AbsentFromSystem: commands' knowledge is synthetic (config-derived), so it
// can never confirm absence. If the flag were set, the engine would skip
// Execute and the stored rollback would never run. Regression for that bug.
func TestBuildPlan_removedCommandExecutesRollback(t *testing.T) {
	cfg := &config.Config{} // command no longer in config
	r := New(cfg, nil)

	st := &state.State{Nodes: make(map[string]state.NodeEntry)}
	st.Set("commands/old-cmd", "somehash", "common", map[string]interface{}{
		"rollback": "rm -f /tmp/x", "sudo": false,
	})

	nodes, err := r.BuildPlan(context.Background(), map[string]bool{}, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Action != engine.ActionRemove {
		t.Fatalf("expected exactly one REMOVE node, got %+v", nodes)
	}
	if nodes[0].AbsentFromSystem {
		t.Fatal("REMOVE node must not be AbsentFromSystem — engine would skip the rollback")
	}
}
