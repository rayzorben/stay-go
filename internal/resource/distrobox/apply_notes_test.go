package distrobox

import (
	"context"
	"strings"
	"testing"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
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
	boxSt.Set("packages/tree", config.Hash("tree"), guestInBoxStateLevel, nil)
	guestPkg := map[string]bool{"packages/tree": true}
	deltas := r.guestWorkDeltas(&entry, guestPkg, true, boxSt)
	if len(deltas) != 0 {
		t.Fatalf("expected no deltas, got %+v", deltas)
	}
}

// Host config layers stamp packages with their source file (e.g. "/users/rayben.yaml"),
// but the per-box state file is written by guest stay-go and uses guestInBoxStateLevel.
// Deltas must not treat that as a perpetual LEVEL drift.
func TestGuestWorkDeltas_hostPackageLevelIgnoredVsBoxState(t *testing.T) {
	cfg := &config.Config{}
	entry := config.DistroboxEntry{
		Name:     "box",
		Packages: []config.PackageEntry{{Name: "tree", SourceFile: "/users/rayben.yaml"}},
	}
	r := New(cfg, &executor.Executor{})
	boxSt := &state.State{Nodes: make(map[string]state.NodeEntry)}
	boxSt.Set("packages/tree", config.Hash("tree"), guestInBoxStateLevel, nil)
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

// A newly added export changes the in-box hash but produces no package/command
// delta. With a reliable guest inventory the apply node must still be UPDATE so
// executeApply runs distrobox-export; it must not be downgraded to TRACK (which
// would save the new hash and silently never export). Regression for that bug.
func TestBuildPlan_addedExportForcesApplyUpdate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	entry := config.DistroboxEntry{Name: "motion", Image: "img", Exports: []string{"code"}}
	cfg := &config.Config{Distrobox: []config.DistroboxEntry{entry}}
	r := New(cfg, &executor.Executor{})

	id := boxNodeID("motion")
	aid := applyNodeID("motion")
	st := &state.State{Nodes: make(map[string]state.NodeEntry)}
	// Box already created and steady-state (host hash matches).
	st.Set(id, distroboxHash(&entry), "", nil)
	// Apply node tracked BEFORE the export was added: old hash, no snapshot.
	st.Set(aid, inBoxHash(&config.DistroboxEntry{Name: "motion"}), "", nil)

	nodes, err := r.BuildPlan(context.Background(), map[string]bool{id: true}, st)
	if err != nil {
		t.Fatal(err)
	}
	var apply *engine.PlanNode
	for _, n := range nodes {
		if n.ID == aid {
			apply = n
		}
	}
	if apply == nil {
		t.Fatal("expected an apply node for the box")
	}
	if apply.Action != engine.ActionUpdate {
		t.Fatalf("expected apply action UPDATE for newly added export, got %v", apply.Action)
	}
}

// Steady state: exports already applied (snapshot matches) with no other pending
// work must downgrade to TRACK so the plan stays quiet.
func TestBuildPlan_unchangedExportStaysTrack(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	entry := config.DistroboxEntry{Name: "motion", Image: "img", Exports: []string{"code"}}
	cfg := &config.Config{Distrobox: []config.DistroboxEntry{entry}}
	r := New(cfg, &executor.Executor{})

	id := boxNodeID("motion")
	aid := applyNodeID("motion")
	st := &state.State{Nodes: make(map[string]state.NodeEntry)}
	st.Set(id, distroboxHash(&entry), "", nil)
	st.Set(aid, inBoxHash(&entry), "", map[string]interface{}{
		"name":             "motion",
		"exports_snapshot": []string{"code"},
	})

	nodes, err := r.BuildPlan(context.Background(), map[string]bool{id: true}, st)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.ID == aid && n.Action != engine.ActionTrack {
			t.Fatalf("expected apply action TRACK for unchanged export, got %v", n.Action)
		}
	}
}

func TestFormatGuestWorkNotes_exportsUpdateUnchangedOmitsLine(t *testing.T) {
	entry := &config.DistroboxEntry{Exports: []string{"a", "b"}}
	notes := formatGuestWorkNotes(entry, nil, true, true, engine.ActionUpdate, []string{"a", "b"})
	if len(notes) != 0 {
		t.Fatalf("expected no notes, got %q", notes)
	}
}

func TestFormatGuestWorkNotes_exportsUpdateShowsDiffOnly(t *testing.T) {
	entry := &config.DistroboxEntry{Exports: []string{"a", "c"}}
	notes := formatGuestWorkNotes(entry, nil, true, true, engine.ActionUpdate, []string{"a", "b"})
	if len(notes) != 1 || !strings.Contains(notes[0], "exports:") {
		t.Fatalf("expected single exports line, got %q", notes)
	}
	if !strings.Contains(notes[0], "+c") || !strings.Contains(notes[0], "-b") {
		t.Fatalf("expected +c and -b in %q", notes[0])
	}
}
