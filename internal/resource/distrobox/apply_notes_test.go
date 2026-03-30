package distrobox

import (
	"strings"
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

// Host config layers stamp nested package Level as user_config etc., but the
// per-box state file is written by guest stay-go and always uses "common".
// Deltas must not treat that as a perpetual LEVEL drift.
func TestGuestWorkDeltas_hostPackageLevelIgnoredVsBoxState(t *testing.T) {
	cfg := &config.Config{}
	entry := config.DistroboxEntry{
		Name:     "box",
		Packages: []config.PackageEntry{{Name: "tree", Level: "user_config"}},
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
