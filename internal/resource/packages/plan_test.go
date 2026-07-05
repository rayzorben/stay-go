package packages

import (
	"context"
	"testing"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/state"
)

// aptLike returns a manager with an UpdateCmd (index sync), like apt/zypper/apk.
func aptLike() *PackageManager {
	return &PackageManager{
		Name:       "apt-get",
		Binary:     "apt-get",
		NeedsSudo:  true,
		UpdateCmd:  []string{"apt-get", "update", "-y"},
		InstallCmd: []string{"apt-get", "install", "-y"},
	}
}

func hasSyncNode(nodes []*engine.PlanNode) bool {
	for _, n := range nodes {
		if isPackageSyncNode(n.ID) {
			return true
		}
	}
	return false
}

// Steady state (package installed + tracked, hash unchanged) must NOT plan an
// index-sync node: the sync is always ADD, so an unconditional node makes every
// up-to-date run execute "apt-get update". Regression for that idempotency bug.
func TestBuildPlan_noSyncNodeWhenNothingInstalls(t *testing.T) {
	cfg := &config.Config{Packages: []config.PackageEntry{{Name: "tree"}}}
	r := New(cfg, nil)
	r.manager = aptLike()

	st := &state.State{Nodes: make(map[string]state.NodeEntry)}
	st.Set("packages/tree", config.Hash("tree"), "common", nil)

	nodes, err := r.BuildPlan(context.Background(), map[string]bool{"packages/tree": true}, st)
	if err != nil {
		t.Fatal(err)
	}
	if hasSyncNode(nodes) {
		t.Fatal("steady-state plan must not contain an index-sync node")
	}
	for _, n := range nodes {
		if n.Action != engine.ActionTrack {
			t.Fatalf("expected only TRACK nodes, got %s for %s", n.Action, n.ID)
		}
	}
}

// A package that will actually install must still get its index-sync node.
func TestBuildPlan_syncNodePlannedForInstall(t *testing.T) {
	cfg := &config.Config{Packages: []config.PackageEntry{{Name: "tree"}}}
	r := New(cfg, nil)
	r.manager = aptLike()

	st := &state.State{Nodes: make(map[string]state.NodeEntry)}
	nodes, err := r.BuildPlan(context.Background(), map[string]bool{}, st)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSyncNode(nodes) {
		t.Fatal("ADD plan must contain an index-sync node for apt-like managers")
	}
}
