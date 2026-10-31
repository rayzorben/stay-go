package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// batchFake records single and batched executions; nodes named "x*" are not
// batchable and IDs in fail fail.
type batchFake struct {
	calls []string
	fail  map[string]bool
}

func (f *batchFake) Type() string { return "fake" }
func (f *batchFake) GatherKnowledge(context.Context) ([]KnowledgeEntry, error) {
	return nil, nil
}
func (f *batchFake) BuildPlan(context.Context, map[string]bool, *state.State) ([]*PlanNode, error) {
	return nil, nil
}
func (f *batchFake) Execute(_ context.Context, n *PlanNode, _ *state.State) error {
	f.calls = append(f.calls, n.ID)
	if f.fail[n.ID] {
		return errors.New("boom")
	}
	return nil
}
func (f *batchFake) BatchKey(n *PlanNode) string {
	if strings.HasPrefix(n.DisplayName, "x") {
		return ""
	}
	return string(n.Action)
}
func (f *batchFake) ExecuteBatch(_ context.Context, nodes []*PlanNode, _ *state.State) []error {
	ids := make([]string, len(nodes))
	errs := make([]error, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
		if f.fail[n.ID] {
			errs[i] = errors.New("boom")
		}
	}
	f.calls = append(f.calls, "["+strings.Join(ids, " ")+"]")
	return errs
}

func fakeNode(name string, deps ...string) *PlanNode {
	return &PlanNode{ID: "fake/" + name, ResourceType: "fake", DisplayName: name, Action: ActionAdd, DependsOn: deps}
}

func runBatchPlan(t *testing.T, f *batchFake, nodes []*PlanNode) error {
	t.Helper()
	e := New(Options{}, &executor.Executor{})
	e.Register(f)
	sorted, err := topoSort(nodes)
	if err != nil {
		t.Fatal(err)
	}
	return e.execute(context.Background(), sorted, &state.State{Nodes: make(map[string]state.NodeEntry)})
}

// Ready nodes are pulled into one batch; a node whose dep has not run yet waits
// and runs after it.
func TestExecute_batchesReadyNodes(t *testing.T) {
	f := &batchFake{}
	nodes := []*PlanNode{
		fakeNode("a"),
		fakeNode("x"),
		fakeNode("c", "fake/x"),
		fakeNode("b"),
		fakeNode("d", "fake/x"),
	}
	if err := runBatchPlan(t, f, nodes); err != nil {
		t.Fatal(err)
	}
	want := "[fake/a fake/b] fake/x [fake/c fake/d]"
	if got := strings.Join(f.calls, " "); got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// A node that failed inside a batch must skip its dependents.
func TestExecute_batchFailurePropagates(t *testing.T) {
	f := &batchFake{fail: map[string]bool{"fake/a": true}}
	nodes := []*PlanNode{fakeNode("a"), fakeNode("b"), fakeNode("xc", "fake/a")}
	if err := runBatchPlan(t, f, nodes); !errors.Is(err, ErrExecutionFailed) {
		t.Fatalf("err = %v, want ErrExecutionFailed", err)
	}
	if got := strings.Join(f.calls, " "); got != "[fake/a fake/b]" {
		t.Fatalf("calls = %q", got)
	}
	if nodes[2].Action != ActionSkip {
		t.Fatalf("dependent of failed batch node should be SKIP, got %s", nodes[2].Action)
	}
}

// Different keys never share a batch; a lone batchable node uses Execute.
func TestExecute_batchKeysSeparate(t *testing.T) {
	f := &batchFake{}
	r := fakeNode("r")
	r.Action = ActionRemove
	nodes := []*PlanNode{fakeNode("a"), r, fakeNode("b")}
	if err := runBatchPlan(t, f, nodes); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls, " "); got != "[fake/a fake/b] fake/r" {
		t.Fatalf("calls = %q", got)
	}
}
