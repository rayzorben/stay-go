package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
	"os"
)

// DistroboxBase holds the fields that a distrobox base file may define.
// Only in-box resources (packages, commands, exports) are inheritable;
// host-level fields (name, image, home, depends) must stay per-entry.
type DistroboxBase struct {
	Packages []PackageEntry `yaml:"packages,omitempty"`
	Commands []CommandEntry `yaml:"commands,omitempty"`
	Exports  []string       `yaml:"exports,omitempty"`
}

// applyDistroboxExtends loads each distrobox entry's extends: file and merges
// the base under the entry. Base items come first; entry-specific items
// override (packages/commands by name, exports are a deduped union).
func applyDistroboxExtends(cfg *Config, vars map[string]string) error {
	for i := range cfg.Distrobox {
		entry := &cfg.Distrobox[i]
		if entry.Extends == "" {
			continue
		}

		path := ResolveString(entry.Extends, vars)
		base, err := loadDistroboxBase(path)
		if err != nil {
			return fmt.Errorf("box %q extends %q: %w", entry.Name, path, err)
		}

		entry.Packages = mergeByKey(base.Packages, entry.Packages, func(p PackageEntry) string { return p.Name })
		entry.Commands = mergeByKey(base.Commands, entry.Commands, func(c CommandEntry) string { return c.Name })
		entry.Exports = mergeStringSlice(base.Exports, entry.Exports)
		entry.Extends = "" // consumed; not needed at runtime
	}
	return nil
}

// loadDistroboxBase reads and decodes a distrobox base YAML file.
func loadDistroboxBase(path string) (*DistroboxBase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", path, err)
	}
	var base DistroboxBase
	if err := yaml.Unmarshal(data, &base); err != nil {
		return nil, fmt.Errorf("parsing %q: %w", path, err)
	}
	return &base, nil
}

// mergeStringSlice returns a deduped union of base and override, base first.
func mergeStringSlice(base, override []string) []string {
	seen := make(map[string]bool, len(base)+len(override))
	out := make([]string, 0, len(base)+len(override))
	for _, s := range append(base, override...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
