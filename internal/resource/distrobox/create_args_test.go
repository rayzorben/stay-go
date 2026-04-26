package distrobox

import (
	"slices"
	"testing"

	"github.com/rayzorben/stay-go/internal/config"
)

func TestBuildCreateArgsRootDefault(t *testing.T) {
	args := buildCreateArgs(&config.DistroboxEntry{
		Name: "b", Image: "img:latest",
	})
	if slices.Contains(args, "--root") {
		t.Fatalf("default should not pass --root: %v", args)
	}
}

func TestBuildCreateArgsRootExplicit(t *testing.T) {
	args := buildCreateArgs(&config.DistroboxEntry{
		Name: "b", Image: "img:latest", Root: true,
	})
	if !slices.Contains(args, "--root") {
		t.Fatalf("root: true should pass --root: %v", args)
	}
}
