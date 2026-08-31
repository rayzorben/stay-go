package engine

import (
	"strings"
	"testing"
)

func updateNodes() []*PlanNode {
	return []*PlanNode{
		{ID: "packages/__upgrade__", ResourceType: "packages", DisplayName: "system upgrade", Action: ActionUpgrade},
		{ID: "containers/frigate", ResourceType: "containers", DisplayName: "frigate", Action: ActionUpgrade, Locked: true},
		{ID: "containers/adguardhome", ResourceType: "containers", DisplayName: "adguardhome", Action: ActionUpgrade},
		{ID: "compose/immich", ResourceType: "compose", DisplayName: "immich", Action: ActionUpgrade},
		{ID: "flatpak/app/org.signal.Signal", ResourceType: "flatpak", DisplayName: "org.signal.Signal", Action: ActionUpgrade},
		{ID: "flatpak/runtimes", ResourceType: "flatpak", DisplayName: "runtimes", Action: ActionUpgrade},
	}
}

func actionsByID(nodes []*PlanNode) map[string]ActionType {
	m := make(map[string]ActionType, len(nodes))
	for _, n := range nodes {
		m[n.ID] = n.Action
	}
	return m
}

func TestFilterUpdateNodesBulkSkipsLocked(t *testing.T) {
	out, err := filterUpdateNodes(updateNodes(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 6 {
		t.Fatalf("expected all 6 nodes, got %d", len(out))
	}
	actions := actionsByID(out)
	if actions["containers/frigate"] != ActionSkip {
		t.Errorf("locked frigate should be SKIP in bulk update, got %s", actions["containers/frigate"])
	}
	if actions["containers/adguardhome"] != ActionUpgrade {
		t.Errorf("unlocked adguardhome should stay UPGRADE, got %s", actions["containers/adguardhome"])
	}
}

func TestFilterUpdateNodesExplicitTargetOverridesLock(t *testing.T) {
	out, err := filterUpdateNodes(updateNodes(), []string{"containers/frigate"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected exactly the targeted node, got %d nodes", len(out))
	}
	if out[0].ID != "containers/frigate" || out[0].Action != ActionUpgrade {
		t.Errorf("explicit target must upgrade despite lock, got %s %s", out[0].ID, out[0].Action)
	}
}

func TestFilterUpdateNodesResourceTypeBulkRespectsLock(t *testing.T) {
	out, err := filterUpdateNodes(updateNodes(), []string{"containers"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 container nodes, got %d", len(out))
	}
	actions := actionsByID(out)
	if actions["containers/frigate"] != ActionSkip {
		t.Errorf("locked frigate should be SKIP in resource-type bulk, got %s", actions["containers/frigate"])
	}
}

func TestFilterUpdateNodesTargetForms(t *testing.T) {
	for _, target := range []string{
		"containers/adguardhome", // node ID
		"containers.adguardhome", // dotted form
		"adguardhome",            // bare unambiguous display name
	} {
		out, err := filterUpdateNodes(updateNodes(), []string{target})
		if err != nil {
			t.Fatalf("target %q: unexpected error: %v", target, err)
		}
		if len(out) != 1 || out[0].ID != "containers/adguardhome" {
			t.Errorf("target %q: expected containers/adguardhome, got %v", target, out)
		}
	}
}

func TestFilterUpdateNodesFlatpakDottedAppID(t *testing.T) {
	// App IDs contain dots; the dotted resource.name form must not swallow them.
	out, err := filterUpdateNodes(updateNodes(), []string{"org.signal.Signal"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID != "flatpak/app/org.signal.Signal" {
		t.Errorf("expected flatpak app node, got %v", out)
	}
	// <resourceType>/<DisplayName> form.
	out, err = filterUpdateNodes(updateNodes(), []string{"flatpak/org.signal.Signal"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID != "flatpak/app/org.signal.Signal" {
		t.Errorf("expected flatpak app node via type/name, got %v", out)
	}
}

func TestFilterUpdateNodesUnknownTarget(t *testing.T) {
	_, err := filterUpdateNodes(updateNodes(), []string{"nonexistent"})
	if err == nil {
		t.Fatal("expected error for unknown target")
	}
	if !strings.Contains(err.Error(), "available:") {
		t.Errorf("error should list available targets, got: %v", err)
	}
}

func TestFilterUpdateNodesAmbiguousBareName(t *testing.T) {
	nodes := updateNodes()
	nodes = append(nodes, &PlanNode{
		ID: "compose/adguardhome", ResourceType: "compose", DisplayName: "adguardhome", Action: ActionUpgrade,
	})
	_, err := filterUpdateNodes(nodes, []string{"adguardhome"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity error, got: %v", err)
	}
}

func TestFilterUpdateNodesMultipleTargets(t *testing.T) {
	out, err := filterUpdateNodes(updateNodes(), []string{"flatpak", "containers/frigate"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	actions := actionsByID(out)
	if len(out) != 3 {
		t.Fatalf("expected 2 flatpak + 1 container node, got %d", len(out))
	}
	if actions["containers/frigate"] != ActionUpgrade {
		t.Errorf("explicitly targeted frigate should upgrade, got %s", actions["containers/frigate"])
	}
}
