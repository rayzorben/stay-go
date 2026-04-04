package packages

import (
	"context"
	"slices"
	"testing"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/state"
)

// TestBuildPlan_perPackageSyncEdges checks each install package gets its own
// index-sync node wired after that package's resource deps (e.g. commands)
// and before the install node — without forcing unrelated packages to wait
// on another package's commands.
func TestBuildPlan_perPackageSyncEdges(t *testing.T) {
	cfg := &config.Config{
		Packages: []config.PackageEntry{
			{Name: "curl"},
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
	syncCurl := byID[packageSyncID("curl")]
	pkgCurl := byID[config.NodeID("packages", "curl")]
	syncEdge := byID[packageSyncID("edge")]
	pkgEdge := byID[config.NodeID("packages", "edge")]
	for _, label := range []struct {
		n   *engine.PlanNode
		msg string
	}{
		{syncCurl, "curl sync"},
		{pkgCurl, "curl package"},
		{syncEdge, "edge sync"},
		{pkgEdge, "edge package"},
	} {
		if label.n == nil {
			t.Fatalf("missing node: %s", label.msg)
		}
	}
	if len(syncCurl.DependsOn) != 0 {
		t.Errorf("curl sync: want no deps, got %v", syncCurl.DependsOn)
	}
	wantEdgeSync := []string{"commands/repo"}
	if !slices.Equal(syncEdge.DependsOn, wantEdgeSync) {
		t.Errorf("edge sync deps: want %v, got %v", wantEdgeSync, syncEdge.DependsOn)
	}
	wantCurlPkg := []string{packageSyncID("curl")}
	if !slices.Equal(pkgCurl.DependsOn, wantCurlPkg) {
		t.Errorf("curl package deps: want %v, got %v", wantCurlPkg, pkgCurl.DependsOn)
	}
	wantEdgePkg := []string{"commands/repo", packageSyncID("edge")}
	if !slices.Equal(pkgEdge.DependsOn, wantEdgePkg) {
		t.Errorf("edge package deps: want %v, got %v", wantEdgePkg, pkgEdge.DependsOn)
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
