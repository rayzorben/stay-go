package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPackageEntryMarshalPreservesDepends(t *testing.T) {
	p := PackageEntry{
		Name:    "microsoft-edge-stable",
		Depends: []map[string][]string{{"commands": {"microsoft-edge-repo"}}},
	}
	b, err := yaml.Marshal(&p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "depends:") || !strings.Contains(s, "microsoft-edge-repo") {
		t.Fatalf("expected depends in marshaled YAML, got:\n%s", s)
	}
	var p2 PackageEntry
	if err := yaml.Unmarshal(b, &p2); err != nil {
		t.Fatal(err)
	}
	if p2.Name != p.Name {
		t.Fatalf("name: got %q", p2.Name)
	}
	if len(p2.Depends) != 1 || len(p2.Depends[0]["commands"]) != 1 || p2.Depends[0]["commands"][0] != "microsoft-edge-repo" {
		t.Fatalf("depends round-trip: %+v", p2.Depends)
	}
}
