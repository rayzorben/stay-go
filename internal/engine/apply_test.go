package engine

import "testing"

func TestNormalizeApplyTarget(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"packages/neovim", "packages/neovim"},
		{"packages.neovim", "packages/neovim"},
		{"commands.command.name=tailscale", "commands/tailscale"},
		{"commands.command.name.tailscale", "commands/tailscale"},
	}

	for _, tc := range cases {
		got, err := normalizeApplyTarget(tc.input)
		if err != nil {
			t.Fatalf("normalizeApplyTarget(%q) unexpected error: %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeApplyTarget(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFilterPlanNodesByApplyTarget(t *testing.T) {
	nodes := []*PlanNode{
		{ID: "packages/git", Action: ActionAdd},
		{ID: "services/docker", Action: ActionAdd, DependsOn: []string{"packages/git"}},
		{ID: "packages/curl", Action: ActionAdd},
	}

	filtered, err := filterPlanNodes(nodes, []string{"services/docker"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("expected 2 filtered nodes, got %d", len(filtered))
	}
	if filtered[0].ID != "packages/git" || filtered[1].ID != "services/docker" {
		t.Fatalf("unexpected filtered order: %v", []string{filtered[0].ID, filtered[1].ID})
	}
}

func TestFilterPlanNodesUnknownTarget(t *testing.T) {
	nodes := []*PlanNode{{ID: "packages/git", Action: ActionAdd}}
	if _, err := filterPlanNodes(nodes, []string{"services/docker"}); err == nil {
		t.Fatal("expected error for unknown apply target")
	}
}
