package commands

import (
	"context"
	"testing"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
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

// An applied command (hash in state) with a check: exit 0 keeps it TRACK, a
// non-zero exit plans an UPDATE that carries the check's first output line.
func TestBuildPlan_checkDrivesRerun(t *testing.T) {
	for _, tc := range []struct {
		check string
		want  engine.ActionType
		desc  string
	}{
		{"true", engine.ActionTrack, ""},
		{"printf 'a\\nb\\nc\\n'; exit 1", engine.ActionUpdate, "check failed, re-run: a (+2 more)"},
	} {
		entry := config.CommandEntry{Name: "c", Command: "echo hi", Check: tc.check}
		r := New(&config.Config{Commands: config.CommandList{entry}}, &executor.Executor{})

		// Seed state with the hash BuildPlan computes, so the command is applied.
		st := &state.State{Nodes: make(map[string]state.NodeEntry)}
		first, err := r.BuildPlan(context.Background(), map[string]bool{"commands/c": true}, st)
		if err != nil {
			t.Fatal(err)
		}
		st.Set("commands/c", first[0].ConfigHash, first[0].SourceFile, nil)

		nodes, err := r.BuildPlan(context.Background(), map[string]bool{"commands/c": true}, st)
		if err != nil {
			t.Fatal(err)
		}
		if nodes[0].Action != tc.want || nodes[0].Description != tc.desc {
			t.Errorf("check %q: got %s %q, want %s %q", tc.check, nodes[0].Action, nodes[0].Description, tc.want, tc.desc)
		}
	}
}
