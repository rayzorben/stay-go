package commands

import (
	"testing"

	"github.com/rayben/stay-go/internal/config"
)

func TestCommandEntryDependsOnSkipsFolders(t *testing.T) {
	e := &config.CommandEntry{
		Name: "c",
		Depends: []map[string][]string{
			{"packages": {"git"}},
			{"folders": {"/tmp/foo"}},
		},
	}
	ids := e.DependsOnIDs()
	if len(ids) != 1 || ids[0] != "packages/git" {
		t.Fatalf("DependsOnIDs = %v, want [packages/git]", ids)
	}
}
