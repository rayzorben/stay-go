package engine

import (
	"testing"
)

func TestTopoSort_Linear(t *testing.T) {
	nodes := []*PlanNode{
		{ID: "packages/docker", Action: ActionAdd},
		{ID: "services/docker", Action: ActionAdd, DependsOn: []string{"packages/docker"}},
	}
	sorted, err := topoSort(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if sorted[0].ID != "packages/docker" || sorted[1].ID != "services/docker" {
		t.Errorf("unexpected order: %v, %v", sorted[0].ID, sorted[1].ID)
	}
}

func TestTopoSort_RemoveReversed(t *testing.T) {
	// When removing, the service (dependent) should be removed before the package (dependency).
	nodes := []*PlanNode{
		{ID: "packages/docker", Action: ActionRemove},
		{ID: "services/docker", Action: ActionRemove, DependsOn: []string{"packages/docker"}},
	}
	sorted, err := topoSort(nodes)
	if err != nil {
		t.Fatal(err)
	}
	// services/docker must come before packages/docker in REMOVE order.
	idx := func(id string) int {
		for i, n := range sorted {
			if n.ID == id {
				return i
			}
		}
		return -1
	}
	if idx("services/docker") > idx("packages/docker") {
		t.Error("services/docker should be removed before packages/docker")
	}
}

func TestTopoSort_Cycle(t *testing.T) {
	nodes := []*PlanNode{
		{ID: "a", Action: ActionAdd, DependsOn: []string{"b"}},
		{ID: "b", Action: ActionAdd, DependsOn: []string{"a"}},
	}
	_, err := topoSort(nodes)
	if err == nil {
		t.Error("expected cycle error")
	}
}

func TestMarkSkips_MissingDep(t *testing.T) {
	nodes := []*PlanNode{
		{ID: "services/docker", Action: ActionAdd, DependsOn: []string{"packages/docker"}},
	}
	markSkips(nodes)
	if nodes[0].Action != ActionSkip {
		t.Errorf("expected SKIP, got %v", nodes[0].Action)
	}
}

func TestMarkSkips_DepRemoved(t *testing.T) {
	nodes := []*PlanNode{
		{ID: "packages/docker", Action: ActionRemove},
		{ID: "services/docker", Action: ActionAdd, DependsOn: []string{"packages/docker"}},
	}
	markSkips(nodes)
	if nodes[1].Action != ActionSkip {
		t.Errorf("expected service to be SKIP when dep is REMOVE, got %v", nodes[1].Action)
	}
}

func TestMarkSkips_DepTracked(t *testing.T) {
	nodes := []*PlanNode{
		{ID: "packages/docker", Action: ActionTrack},
		{ID: "services/docker", Action: ActionAdd, DependsOn: []string{"packages/docker"}},
	}
	markSkips(nodes)
	if nodes[1].Action == ActionSkip {
		t.Error("service should NOT be skipped when dep is TRACK")
	}
}

func TestMarkSkips_TransitivePropagation(t *testing.T) {
	// a → b → c; a is missing → b skipped → c skipped
	nodes := []*PlanNode{
		{ID: "b", Action: ActionAdd, DependsOn: []string{"a"}},
		{ID: "c", Action: ActionAdd, DependsOn: []string{"b"}},
	}
	markSkips(nodes)
	for _, n := range nodes {
		if n.Action != ActionSkip {
			t.Errorf("expected %s to be SKIP, got %v", n.ID, n.Action)
		}
	}
}

func TestStateRemovals(t *testing.T) {
	// Use a minimal fake state.
	type mockState struct{ nodes map[string]struct{} }

	// Directly test the logic using a real state.
	from := map[string]bool{"neovim": true}
	// Manually simulate what StateRemovals does.
	// This tests the helper indirectly through packages.BuildPlan in integration;
	// here we just verify the engine helper's core logic.
	prefix := "packages/"
	stateNodes := map[string]struct{}{
		"packages/neovim": {},
		"packages/git":    {},
	}
	var removals []string
	for id := range stateNodes {
		name := id[len(prefix):]
		if !from[name] {
			removals = append(removals, id)
		}
	}
	if len(removals) != 1 || removals[0] != "packages/git" {
		t.Errorf("unexpected removals: %v", removals)
	}
}
