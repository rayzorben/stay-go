package packages

import (
	"context"
	"slices"
	"testing"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
)

// TestBuildPlan_sharedSyncNodes checks that packages with the same dependency
// set share a single sync node, and packages with different deps get separate
// sync nodes — so apt-get update runs once per dep group, not once per package.
func TestBuildPlan_sharedSyncNodes(t *testing.T) {
	cfg := &config.Config{
		Packages: []config.PackageEntry{
			{Name: "curl"},
			{Name: "wget"},
			{
				Name:    "edge",
				Depends: []map[string][]string{{"commands": {"repo"}}},
			},
		},
	}
	r := New(cfg, nil)
	r.manager = &PackageManager{
		Name:      "apt-get",
		UpdateCmd: []string{"apt-get", "update", "-y"},
	}
	nodes, err := r.BuildPlan(context.Background(), map[string]bool{}, emptyState(t))
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]*engine.PlanNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}

	// curl and wget share the no-deps sync node.
	syncNodeps := byID[packageSyncGroupID("")]
	pkgCurl := byID[config.NodeID("packages", "curl")]
	pkgWget := byID[config.NodeID("packages", "wget")]

	// edge gets its own sync node because it has a different dep set.
	syncEdge := byID[packageSyncGroupID("commands/repo")]
	pkgEdge := byID[config.NodeID("packages", "edge")]

	for _, label := range []struct {
		n   *engine.PlanNode
		msg string
	}{
		{syncNodeps, "no-deps sync"},
		{pkgCurl, "curl package"},
		{pkgWget, "wget package"},
		{syncEdge, "edge sync"},
		{pkgEdge, "edge package"},
	} {
		if label.n == nil {
			t.Fatalf("missing node: %s", label.msg)
		}
	}

	// Exactly two sync nodes total (one for nodeps group, one for edge group).
	var syncNodes []*engine.PlanNode
	for _, n := range nodes {
		if isPackageSyncNode(n.ID) {
			syncNodes = append(syncNodes, n)
		}
	}
	if len(syncNodes) != 2 {
		t.Errorf("want 2 sync nodes, got %d", len(syncNodes))
	}

	// Sync nodes are hidden.
	if !syncNodeps.Hidden {
		t.Error("no-deps sync node should be Hidden")
	}
	if !syncEdge.Hidden {
		t.Error("edge sync node should be Hidden")
	}

	// No-deps sync has no dependencies.
	if len(syncNodeps.DependsOn) != 0 {
		t.Errorf("no-deps sync: want no deps, got %v", syncNodeps.DependsOn)
	}

	// Edge sync depends on the command dep.
	wantEdgeSyncDeps := []string{"commands/repo"}
	if !slices.Equal(syncEdge.DependsOn, wantEdgeSyncDeps) {
		t.Errorf("edge sync deps: want %v, got %v", wantEdgeSyncDeps, syncEdge.DependsOn)
	}

	// curl and wget both depend on the shared no-deps sync.
	wantNodepsPkg := []string{packageSyncGroupID("")}
	if !slices.Equal(pkgCurl.DependsOn, wantNodepsPkg) {
		t.Errorf("curl deps: want %v, got %v", wantNodepsPkg, pkgCurl.DependsOn)
	}
	if !slices.Equal(pkgWget.DependsOn, wantNodepsPkg) {
		t.Errorf("wget deps: want %v, got %v", wantNodepsPkg, pkgWget.DependsOn)
	}

	// edge depends on its own sync (after commands/repo).
	wantEdgePkg := []string{"commands/repo", packageSyncGroupID("commands/repo")}
	if !slices.Equal(pkgEdge.DependsOn, wantEdgePkg) {
		t.Errorf("edge deps: want %v, got %v", wantEdgePkg, pkgEdge.DependsOn)
	}
}

func emptyState(t *testing.T) *state.State {
	t.Helper()
	st, err := state.Load("/nonexistent/stay-go-state-test-" + t.Name())
	if err != nil {
		t.Fatal(err)
	}
	return st
}
