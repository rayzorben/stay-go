package services

import (
	"context"
	"testing"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
)

// A service removed from config must produce a REMOVE node that is NOT marked
// AbsentFromSystem: services' knowledge only probes configured entries, so a
// removed service is never in knowledge regardless of its real enabled state.
// If the flag were set, the engine would skip Execute and the service would
// never be disabled. Regression for that bug.
func TestBuildPlan_removedServiceIsNotAbsentFromSystem(t *testing.T) {
	cfg := &config.Config{} // service no longer in config
	r := New(cfg, nil)

	st := &state.State{Nodes: make(map[string]state.NodeEntry)}
	st.Set("services/old-svc", "somehash", "common", map[string]interface{}{
		"service": "old-svc", "user": false, "enabled": true,
	})

	nodes, err := r.BuildPlan(context.Background(), map[string]bool{}, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Action != engine.ActionRemove {
		t.Fatalf("expected exactly one REMOVE node, got %+v", nodes)
	}
	if nodes[0].AbsentFromSystem {
		t.Fatal("REMOVE node must not be AbsentFromSystem — engine would skip the disable")
	}
}

// serviceFromState must reconstruct name and scope from persisted state data.
func TestServiceFromState(t *testing.T) {
	st := &state.State{Nodes: make(map[string]state.NodeEntry)}
	st.Set("services/pipewire", "h", "common", map[string]interface{}{
		"service": "pipewire", "user": true,
	})
	node := &engine.PlanNode{ID: "services/pipewire", DisplayName: "pipewire"}
	name, isUser := serviceFromState(node, st)
	if name != "pipewire" || !isUser {
		t.Fatalf("got name=%q isUser=%v", name, isUser)
	}
}
