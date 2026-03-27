package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// includeEntry records a top-level !include directive found in a config file.
type includeEntry struct {
	keyName  string // top-level key that carried the tag, e.g. "ghostty"
	filePath string // resolved absolute path to the included file
}

// loadLayer reads the YAML config at path, stamps all direct entries with
// level, processes top-level `key: !include "file"` directives (stamping
// their entries with level+":"+keyName), and returns the merged result.
//
// visited prevents circular includes; keys are absolute paths.
func loadLayer(path, level string, visited map[string]bool) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving path %q: %w", path, err)
	}
	if visited[abs] {
		return &Config{Vars: make(map[string]string)}, nil
	}
	visited[abs] = true

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing config %q: %w", path, err)
	}

	cfg, includes, err := extractIncludes(&doc, filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("extracting includes from %q: %w", path, err)
	}

	// Stamp the source file on every secret entry so the Manager knows which
	// file to rewrite when encrypting plaintext values in-place.
	for k := range cfg.Secrets {
		e := cfg.Secrets[k]
		e.SourceFile = abs
		cfg.Secrets[k] = e
	}

	// Stamp direct entries with this layer's level (includes secrets Level).
	stampLevel(cfg, level)

	// Load included files as base; this file's direct entries override.
	result := cfg
	for _, inc := range includes {
		incLevel := inc.keyName
		if level != "" {
			incLevel = level + ":" + inc.keyName
		}
		incCfg, err := loadLayer(inc.filePath, incLevel, visited)
		if err != nil {
			return nil, fmt.Errorf("include %q (key %q) in %q: %w", inc.filePath, inc.keyName, path, err)
		}
		result = mergeConfigs(incCfg, result)
	}

	return result, nil
}

// loadLayerOptional calls loadLayer but returns nil without error when the
// file does not exist.
func loadLayerOptional(path, level string, visited map[string]bool) (*Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	return loadLayer(path, level, visited)
}

// extractIncludes parses a *yaml.Node document, removes top-level key-value
// pairs whose value carries the !include tag, decodes the remainder into a
// Config, and returns both the Config and the collected include entries.
func extractIncludes(doc *yaml.Node, dir string) (*Config, []includeEntry, error) {
	cfg := &Config{Vars: make(map[string]string)}
	if doc == nil || (doc.Kind == yaml.DocumentNode && len(doc.Content) == 0) {
		return cfg, nil, nil
	}

	var mapping *yaml.Node
	if doc.Kind == yaml.DocumentNode {
		mapping = doc.Content[0]
	} else {
		mapping = doc
	}
	if mapping.Kind != yaml.MappingNode {
		return cfg, nil, nil
	}

	var includes []includeEntry
	var remaining []*yaml.Node

	// Mapping children alternate: key₀, val₀, key₁, val₁, …
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		keyNode := mapping.Content[i]
		valNode := mapping.Content[i+1]

		if valNode.Tag == "!include" {
			filePath := resolveIncludePath(valNode.Value, dir)
			if !filepath.IsAbs(filePath) {
				filePath = filepath.Join(dir, filePath)
			}
			includes = append(includes, includeEntry{
				keyName:  resolveIncludePath(keyNode.Value, dir),
				filePath: filePath,
			})
		} else {
			remaining = append(remaining, keyNode, valNode)
		}
	}
	mapping.Content = remaining

	if err := doc.Decode(cfg); err != nil {
		return nil, nil, err
	}

	return cfg, includes, nil
}

// resolveIncludePath resolves ${config_root}, ${env:VAR}, $(command), and ~
// in an include path or key name. configRoot is the directory of the file
// containing the include directive (equivalent to ${config_root} at that level).
func resolveIncludePath(s, configRoot string) string {
	home, _ := os.UserHomeDir()
	s = strings.ReplaceAll(s, "${config_root}", configRoot)
	s = strings.ReplaceAll(s, "~", home)
	s = resolveEnvVars(s)
	s = resolveCommandSubs(s)
	return s
}

// stampLevel sets the Level field on every entry in cfg.
// An empty level (the default.yaml root) is mapped to "common" so that direct
// entries in default.yaml display and store as "common", preserving backward
// compatibility with state written by the old explicit-layer approach.
func stampLevel(cfg *Config, level string) {
	if level == "" {
		level = "common"
	}
	for i := range cfg.Packages {
		cfg.Packages[i].Level = level
	}
	for i := range cfg.Groups {
		cfg.Groups[i].Level = level
	}
	for i := range cfg.Users {
		cfg.Users[i].Level = level
	}
	for i := range cfg.Services {
		cfg.Services[i].Level = level
	}
	for i := range cfg.Scripts {
		cfg.Scripts[i].Level = level
	}
	for i := range cfg.Files {
		cfg.Files[i].Level = level
	}
	for i := range cfg.Commands {
		cfg.Commands[i].Level = level
	}
	for i := range cfg.Containers {
		cfg.Containers[i].Level = level
	}
	for i := range cfg.Distrobox {
		cfg.Distrobox[i].Level = level
		for j := range cfg.Distrobox[i].Packages {
			cfg.Distrobox[i].Packages[j].Level = level
		}
		for j := range cfg.Distrobox[i].Commands {
			cfg.Distrobox[i].Commands[j].Level = level
		}
	}
	for k := range cfg.Secrets {
		e := cfg.Secrets[k]
		e.Level = level
		cfg.Secrets[k] = e
	}
}

// mergeConfigs merges override on top of base.
// For each resource type, override entries with the same identity key replace
// the corresponding base entry; new entries from both are preserved.
// Variable maps and secrets maps are merged with the same precedence (override wins).
func mergeConfigs(base, override *Config) *Config {
	result := &Config{
		Vars:    make(map[string]string),
		Secrets: make(SecretsMap),
	}

	for k, v := range base.Vars {
		result.Vars[k] = v
	}
	for k, v := range override.Vars {
		result.Vars[k] = v
	}

	for k, v := range base.Secrets {
		result.Secrets[k] = v
	}
	for k, v := range override.Secrets {
		result.Secrets[k] = v
	}

	result.Packages = mergeByKey(base.Packages, override.Packages,
		func(p PackageEntry) string { return p.Name })

	result.Groups = mergeByKey(base.Groups, override.Groups,
		func(g GroupEntry) string { return g.Name })

	result.Users = mergeByKey(base.Users, override.Users,
		func(u UserEntry) string { return u.Username })

	result.Services = mergeByKey(base.Services, override.Services,
		func(s ServiceEntry) string { return s.Service })

	result.Scripts = mergeByKey(base.Scripts, override.Scripts,
		func(s ScriptEntry) string { return s.Script })

	result.Files = mergeByKey(base.Files, override.Files,
		func(f FileEntry) string { return f.Target })

	result.Commands = mergeByKey(base.Commands, override.Commands,
		func(c CommandEntry) string { return c.Name })

	result.Containers = mergeByKey(base.Containers, override.Containers,
		func(c ContainerEntry) string { return c.ContainerName() })

	result.Distrobox = mergeByKey(base.Distrobox, override.Distrobox,
		func(d DistroboxEntry) string { return d.Name })

	return result
}

// mergeByKey merges two slices where override entries with matching keys replace
// base entries. Order: base entries first (in original order), then any
// override entries whose key was not in base.
func mergeByKey[T any](base, override []T, key func(T) string) []T {
	idx := make(map[string]int, len(base))
	result := make([]T, len(base))
	copy(result, base)
	for i, v := range base {
		idx[key(v)] = i
	}
	for _, v := range override {
		k := key(v)
		if i, ok := idx[k]; ok {
			result[i] = v
		} else {
			idx[k] = len(result)
			result = append(result, v)
		}
	}
	return result
}
