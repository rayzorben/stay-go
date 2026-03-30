package distrobox

import (
	"testing"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/executor"
	"github.com/rayben/stay-go/internal/state"
)

func TestGuestWorkDeltas_missingPackageIsAdd(t *testing.T) {
	cfg := &config.Config{}
	entry := config.DistroboxEntry{
		Name:     "box",
		Packages: []config.PackageEntry{{Name: "tree"}},
	}
	r := New(cfg, &executor.Executor{})
	boxSt := &state.State{Nodes: make(map[string]state.NodeEntry)}
	guestPkg := map[string]bool{}
	deltas := r.guestWorkDeltas(&entry, guestPkg, true, boxSt)
	if len(deltas) != 1 || deltas[0].kind != "packages" || deltas[0].name != "tree" || deltas[0].action != engine.ActionAdd {
		t.Fatalf("got %+v", deltas)
	}
}

func TestGuestWorkDeltas_trackedInstalledPackageEmitsNothing(t *testing.T) {
	cfg := &config.Config{}
	entry := config.DistroboxEntry{
		Name:     "box",
		Packages: []config.PackageEntry{{Name: "tree"}},
	}
	r := New(cfg, &executor.Executor{})
	boxSt := &state.State{Nodes: make(map[string]state.NodeEntry)}
	boxSt.Set("packages/tree", config.Hash("tree"), "common", nil)
	guestPkg := map[string]bool{"packages/tree": true}
	deltas := r.guestWorkDeltas(&entry, guestPkg, true, boxSt)
	if len(deltas) != 0 {
		t.Fatalf("expected no deltas, got %+v", deltas)
	}
}

func TestGuestWorkPending(t *testing.T) {
	if guestWorkPending([]guestDelta{{action: engine.ActionTrack}}) {
		t.Fatal("TRACK should not count as pending")
	}
	if !guestWorkPending([]guestDelta{{action: engine.ActionAdd}}) {
		t.Fatal("ADD should count as pending")
	}
}
