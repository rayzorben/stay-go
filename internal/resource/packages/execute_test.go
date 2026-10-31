package packages

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// stubBin puts a fake binary on PATH that appends its argv to a call log and
// prints stdout. Returns the call log path.
func stubBin(t *testing.T, dir, name, stdout string, exit int) string {
	t.Helper()
	logPath := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"" + name + " $*\" >> '" + logPath + "'\n"
	if stdout != "" {
		script += "printf '%s' '" + stdout + "'\n"
	}
	script += "exit " + string(rune('0'+exit)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return logPath
}

func readCalls(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return splitLines(string(b))
}

// Removing winboat must first mark configured freerdp explicit, or pacman -Rs
// takes it along as an orphaned dependency. Regression for the cachygram run.
func TestExecuteRemove_marksConfiguredPackagesExplicitFirst(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// pacman -Qq resolves the virtual "zen-browser" to its DB name and exits
	// 1 (as if some name were not found) — the found names must still be used.
	calls := stubBin(t, dir, "pacman", "freerdp\nzen-browser-bin\n", 1)
	stubBin(t, dir, "paru", "", 0)

	cfg := &config.Config{Packages: []config.PackageEntry{
		{Name: "freerdp"},
		{Name: "zen-browser"},
		{Name: "not-installed"},
		{Name: "gone", Remove: true},
	}}
	r := New(cfg, &executor.Executor{})
	m := managers[0] // paru
	r.manager = &m

	st := &state.State{Nodes: make(map[string]state.NodeEntry)}
	st.Set("packages/winboat", config.Hash("winboat"), "common", nil)
	knowledge := map[string]bool{
		"packages/freerdp":     true,
		"packages/zen-browser": true,
		"packages/winboat":     true,
		"packages/gone":        true,
	}
	nodes, err := r.BuildPlan(context.Background(), knowledge, st)
	if err != nil {
		t.Fatal(err)
	}
	var rm *engine.PlanNode
	for _, n := range nodes {
		if n.ID == "packages/winboat" && n.Action == engine.ActionRemove {
			rm = n
		}
	}
	if rm == nil {
		t.Fatal("expected REMOVE node for winboat")
	}
	if err := r.Execute(context.Background(), rm, st); err != nil {
		t.Fatal(err)
	}

	// paru and pacman stubs share one call log, so order is preserved.
	got := readCalls(t, calls)
	want := []string{
		"pacman -Qq freerdp zen-browser",
		"paru -D --asexplicit freerdp zen-browser-bin",
		"paru -Rs --noconfirm winboat",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("calls:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if _, ok := st.Nodes["packages/winboat"]; ok {
		t.Fatal("winboat should be deleted from state")
	}
}

// If configured packages cannot be protected, the removal must not run.
func TestExecuteRemove_markFailureAbortsRemoval(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	calls := stubBin(t, dir, "apt-mark", "", 1)
	stubBin(t, dir, "apt-get", "", 0)

	cfg := &config.Config{Packages: []config.PackageEntry{{Name: "freerdp"}}}
	r := New(cfg, &executor.Executor{})
	r.manager = &PackageManager{
		Name:            "apt-get",
		RemoveCmd:       []string{"apt-get", "remove", "-y", "--autoremove"},
		MarkExplicitCmd: []string{"apt-mark", "manual"},
	}
	r.installed = map[string]bool{"packages/freerdp": true, "packages/winboat": true}

	st := &state.State{Nodes: make(map[string]state.NodeEntry)}
	st.Set("packages/winboat", "h", "common", nil)
	node := &engine.PlanNode{ID: "packages/winboat", DisplayName: "winboat", Action: engine.ActionRemove}
	if err := r.Execute(context.Background(), node, st); err == nil {
		t.Fatal("expected error when marking fails")
	}
	if got := readCalls(t, calls); !slices.Equal(got, []string{"apt-mark manual freerdp"}) {
		t.Fatalf("remove must not run after a failed mark; calls: %v", got)
	}
	if _, ok := st.Nodes["packages/winboat"]; !ok {
		t.Fatal("winboat must stay in state when removal is aborted")
	}
}

func TestProtectedPackages(t *testing.T) {
	pkgs := []config.PackageEntry{
		{Name: "git"}, {Name: "freerdp"}, {Name: "freerdp"},
		{Name: "absent"}, {Name: "winboat"}, {Name: "old", Remove: true},
	}
	installed := map[string]bool{
		"packages/git": true, "packages/freerdp": true,
		"packages/winboat": true, "packages/old": true,
	}
	got := protectedPackages(pkgs, installed, "winboat")
	if want := []string{"freerdp", "git"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolvedNames(t *testing.T) {
	got := resolvedNames("a\nb\na\nwinboat\n", "winboat")
	if want := []string{"a", "b"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Every manager whose remove can take unrequested dependencies must either
// protect configured packages or disable that cleanup.
func TestManagers_removeKeepsConfiguredDeps(t *testing.T) {
	for _, m := range managers {
		rm := strings.Join(m.RemoveCmd, " ")
		switch {
		case strings.Contains(rm, " -Rs"), strings.Contains(rm, "--autoremove"), m.Name == "apk":
			if m.MarkExplicitCmd == nil {
				t.Errorf("%s: %q removes orphaned deps but has no MarkExplicitCmd", m.Name, rm)
			}
		case m.Name == "dnf":
			if !strings.Contains(rm, "clean_requirements_on_remove=False") {
				t.Errorf("dnf: %q must disable clean_requirements_on_remove", rm)
			}
		}
	}
}
