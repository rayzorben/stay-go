package files

import (
	"context"
	"testing"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/state"
)

func TestBuildPlan_AdditiveSnippetChangeUsesUpdate(t *testing.T) {
	cfg := &config.Config{
		Files: []config.FileEntry{{
			Target:  "/tmp/stay-go-additive-test",
			Content: "include \"rules.kdl\"\n",
			Add:     true,
		}},
	}
	st := &state.State{
		Nodes: map[string]state.NodeEntry{
			"files//tmp/stay-go-additive-test": {Hash: "stale-hash-not-equal"},
		},
	}
	r := New(cfg, nil)
	nodes, err := r.BuildPlan(context.Background(), map[string]bool{
		"files//tmp/stay-go-additive-test": false,
	}, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes: %d", len(nodes))
	}
	if nodes[0].Action != engine.ActionUpdate {
		t.Fatalf("got action %q, want UPDATE", nodes[0].Action)
	}
}
