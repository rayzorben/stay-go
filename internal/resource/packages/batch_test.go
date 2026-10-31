package packages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// fakeManager returns a shell-backed manager: every invocation is appended to
// calls, installed packages are tracked in a file, and a package named "bad"
// fails the whole transaction (like pacman). Named "paru" so manager-priority
// checks treat it as the top-ranked manager.
func fakeManager(t *testing.T) (*PackageManager, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	inst := filepath.Join(dir, "installed")
	if err := os.WriteFile(inst, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	install := `echo "install $*" >> ` + calls + `; for p; do [ "$p" = bad ] && exit 1; done; for p; do echo "$p" >> ` + inst + `; done`
	remove := `echo "remove $*" >> ` + calls + `; for p; do [ "$p" = bad ] && exit 1; done; for p; do sed -i "/^$p\$/d" ` + inst + `; done`
	return &PackageManager{
		Name:       "paru",
		Binary:     "paru",
		ListCmd:    []string{"cat", inst},
		InstallCmd: []string{"sh", "-c", install, "fake"},
		RemoveCmd:  []string{"sh", "-c", remove, "fake"},
	}, calls
}

func readCalls(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return splitLines(string(b))
}

func pkgNode(name string, action engine.ActionType) *engine.PlanNode {
	return &engine.PlanNode{
		ID: config.NodeID("packages", name), ResourceType: "packages",
		DisplayName: name, Action: action, ConfigHash: config.Hash(name),
	}
}

func newBatchResource(t *testing.T) (*Resource, string, *state.State) {
	m, calls := fakeManager(t)
	r := New(&config.Config{}, &executor.Executor{})
	r.manager = m
	return r, calls, &state.State{Nodes: make(map[string]state.NodeEntry)}
}

func TestExecuteBatch_singleTransaction(t *testing.T) {
	r, calls, st := newBatchResource(t)
	nodes := []*engine.PlanNode{pkgNode("a", engine.ActionAdd), pkgNode("b", engine.ActionAdd), pkgNode("c", engine.ActionAdd)}

	for i, err := range r.ExecuteBatch(context.Background(), nodes, st) {
		if err != nil {
			t.Fatalf("node %s: %v", nodes[i].ID, err)
		}
	}
	if got := readCalls(t, calls); len(got) != 1 || got[0] != "install a b c" {
		t.Fatalf("want one batched install, got %q", got)
	}
	for _, n := range nodes {
		if _, ok := st.Get(n.ID); !ok {
			t.Fatalf("%s not recorded in state", n.ID)
		}
	}
}

func TestExecuteBatch_fallbackIsolatesFailure(t *testing.T) {
	r, calls, st := newBatchResource(t)
	nodes := []*engine.PlanNode{pkgNode("a", engine.ActionAdd), pkgNode("bad", engine.ActionAdd), pkgNode("c", engine.ActionAdd)}

	errs := r.ExecuteBatch(context.Background(), nodes, st)
	if errs[0] != nil || errs[2] != nil {
		t.Fatalf("unrelated packages must succeed: %v", errs)
	}
	if errs[1] == nil {
		t.Fatal("bad package must fail")
	}
	want := []string{"install a bad c", "install a", "install bad", "install c"}
	if got := readCalls(t, calls); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if _, ok := st.Get("packages/bad"); ok {
		t.Fatal("failed package must not be recorded in state")
	}
	if _, ok := st.Get("packages/c"); !ok {
		t.Fatal("packages/c not recorded in state")
	}
}

func TestExecuteBatch_remove(t *testing.T) {
	r, calls, st := newBatchResource(t)
	st.Set("packages/old", config.Hash("old"), "", nil)
	st.Set("packages/gone", config.Hash("gone"), "", nil)
	nodes := []*engine.PlanNode{pkgNode("old", engine.ActionRemove), pkgNode("gone", engine.ActionRemove)}

	for i, err := range r.ExecuteBatch(context.Background(), nodes, st) {
		if err != nil {
			t.Fatalf("node %s: %v", nodes[i].ID, err)
		}
	}
	if got := readCalls(t, calls); len(got) != 1 || got[0] != "remove old gone" {
		t.Fatalf("want one batched remove, got %q", got)
	}
	if len(st.Nodes) != 0 {
		t.Fatalf("state should be empty, got %v", st.Nodes)
	}
}

func TestBatchKey(t *testing.T) {
	r := New(&config.Config{}, nil)
	cases := []struct {
		node *engine.PlanNode
		want string
	}{
		{pkgNode("a", engine.ActionAdd), batchInstall},
		{pkgNode("a", engine.ActionUpdate), batchInstall},
		{pkgNode("a", engine.ActionRemove), batchRemove},
		{pkgNode("a", engine.ActionTrack), ""},
		{&engine.PlanNode{ID: syncNodePrefix + "nodeps", Action: engine.ActionAdd}, ""},
		{&engine.PlanNode{ID: upgradeNodeID, Action: engine.ActionUpgrade}, ""},
	}
	for _, c := range cases {
		if got := r.BatchKey(c.node); got != c.want {
			t.Errorf("BatchKey(%s %s) = %q, want %q", c.node.Action, c.node.ID, got, c.want)
		}
	}
}

func TestIsBetterManager(t *testing.T) {
	r := New(&config.Config{}, nil)
	r.manager = &managers[2] // pacman
	for name, want := range map[string]bool{"paru": true, "paru-bin": true, "yay-git": true, "neovim": false, "pacman-contrib": false} {
		if got := r.isBetterManager(name); got != want {
			t.Errorf("isBetterManager(%q) = %v, want %v", name, got, want)
		}
	}
}
