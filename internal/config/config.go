// Package config handles loading and parsing the YAML desired-state configuration.
// It also provides deterministic hashing of config nodes, used to detect changes
// between runs and drive the UPDATE action.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Config is the top-level structure for the desired system state.
type Config struct {
	Vars     map[string]string `yaml:"variables"`
	Packages []PackageEntry    `yaml:"packages"`
	Groups   []GroupEntry      `yaml:"groups"`
	Users    []UserEntry       `yaml:"users"`
	Services []ServiceEntry    `yaml:"services"`
	Scripts  []ScriptEntry     `yaml:"scripts"`
	Commands []CommandEntry    `yaml:"commands"`
	Secrets  SecretsMap        `yaml:"secrets"`

	// DecryptedSecrets holds the plaintext values of all secrets after the
	// Manager has processed them. Populated by cmd/stay-go after LoadAll;
	// not serialised to YAML. Keyed by the secret name (without "secrets." prefix).
	DecryptedSecrets map[string]string `yaml:"-"`
}

// CommandEntry defines a named inline command managed by stay-go.
// The command string is executed via bash; rollback is run on removal.
type CommandEntry struct {
	Name     string                `yaml:"name"`
	Command  string                `yaml:"command"`
	Rollback string                `yaml:"rollback,omitempty"`
	Sudo     bool                  `yaml:"sudo,omitempty"`
	Depends  []map[string][]string `yaml:"depends,omitempty"`
	Level    string                `yaml:"-"` // set by LoadAll, not parsed from YAML
}

// DependsOnIDs converts the raw depends field into canonical resource node IDs,
// skipping the special "files" key which is handled as a condition at plan time.
func (e *CommandEntry) DependsOnIDs() []string {
	var ids []string
	for _, dep := range e.Depends {
		for resourceType, names := range dep {
			if resourceType == "files" {
				continue
			}
			for _, name := range names {
				ids = append(ids, resourceType+"/"+name)
			}
		}
	}
	return ids
}

// FileConditions returns the file existence conditions declared in depends.
// A path prefixed with "!" means the file must NOT exist; otherwise it must exist.
func (e *CommandEntry) FileConditions() []string {
	for _, dep := range e.Depends {
		if files, ok := dep["files"]; ok {
			return files
		}
	}
	return nil
}

// ScriptEntry defines a shell script to run as part of the desired state.
type ScriptEntry struct {
	Script  string                `yaml:"script"`
	Sudo    bool                  `yaml:"sudo,omitempty"`
	Depends []map[string][]string `yaml:"depends,omitempty"`
	Level   string                `yaml:"-"` // set by LoadAll, not parsed from YAML
}

// DependsOnIDs converts the raw depends field into canonical resource node IDs,
// skipping the special "folders" key which is handled at plan time.
func (s *ScriptEntry) DependsOnIDs() []string {
	var ids []string
	for _, dep := range s.Depends {
		for resourceType, names := range dep {
			if resourceType == "folders" {
				continue
			}
			for _, name := range names {
				ids = append(ids, resourceType+"/"+name)
			}
		}
	}
	return ids
}

// FolderConditions returns the folder conditions declared in depends.
// A path starting with "!" means the folder must NOT exist; otherwise it must exist.
func (s *ScriptEntry) FolderConditions() []string {
	for _, dep := range s.Depends {
		if folders, ok := dep["folders"]; ok {
			return folders
		}
	}
	return nil
}

// GroupEntry represents a single system group to manage. Supports both scalar
// ("wheel") and mapping ({ name: wheel }) forms in YAML.
type GroupEntry struct {
	Name  string
	Level string `yaml:"-"` // set by LoadAll, not parsed from YAML
}

// UnmarshalYAML implements yaml.Unmarshaler to accept both scalar and map forms.
func (g *GroupEntry) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		g.Name = value.Value
		return nil
	case yaml.MappingNode:
		var m struct {
			Name string `yaml:"name"`
		}
		if err := value.Decode(&m); err != nil {
			return fmt.Errorf("decoding group entry: %w", err)
		}
		g.Name = m.Name
		return nil
	default:
		return fmt.Errorf("unexpected YAML node kind %v for group entry", value.Kind)
	}
}

// PackageEntry represents a single package to manage. Supports both scalar
// ("neovim") and mapping ({ name: neovim }) forms in YAML.
type PackageEntry struct {
	Name  string
	Level string `yaml:"-"` // set by LoadAll, not parsed from YAML
}

// UnmarshalYAML implements yaml.Unmarshaler to accept both scalar and map forms.
func (p *PackageEntry) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		p.Name = value.Value
		return nil
	case yaml.MappingNode:
		var m struct {
			Name string `yaml:"name"`
		}
		if err := value.Decode(&m); err != nil {
			return fmt.Errorf("decoding package entry: %w", err)
		}
		p.Name = m.Name
		return nil
	default:
		return fmt.Errorf("unexpected YAML node kind %v for package entry", value.Kind)
	}
}

// UserEntry defines the desired state of a system user.
type UserEntry struct {
	Username string   `yaml:"username"`
	Name     string   `yaml:"name,omitempty"` // full display name (GECOS)
	Shell    string   `yaml:"shell,omitempty"`
	Home     string   `yaml:"home,omitempty"`
	UID      string   `yaml:"uid,omitempty"`
	Groups   []string `yaml:"groups,omitempty"`
	Level    string   `yaml:"-"` // set by LoadAll, not parsed from YAML
}

// ServiceEntry defines the desired state of a systemd service.
type ServiceEntry struct {
	Service string                `yaml:"service"`
	User    bool                  `yaml:"user,omitempty"`    // true = systemd user service
	Enabled *bool                 `yaml:"enabled,omitempty"` // default true
	Depends []map[string][]string `yaml:"depends,omitempty"` // [{packages: [docker]}]
	Level   string                `yaml:"-"`                 // set by LoadAll, not parsed from YAML
}

// IsEnabled returns the effective enabled state (defaults to true when unset).
func (s *ServiceEntry) IsEnabled() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// DependsOnIDs converts the raw depends field into canonical resource node IDs.
// [{packages: [docker, git]}] → ["packages/docker", "packages/git"]
func (s *ServiceEntry) DependsOnIDs() []string {
	var ids []string
	for _, dep := range s.Depends {
		for resourceType, names := range dep {
			for _, name := range names {
				ids = append(ids, resourceType+"/"+name)
			}
		}
	}
	return ids
}

// Load reads and parses the YAML configuration file at path.
// Top-level `key: !include "file"` directives are resolved and merged recursively.
func Load(path string) (*Config, error) {
	return loadLayer(path, "", make(map[string]bool))
}

// LoadAll loads and merges config from all applicable layers:
//
//   - <configDir>/default.yaml            (level: "common")
//   - <configDir>/hosts/<hostname>.yaml   (level: "host:<hostname>")
//   - <configDir>/users/<username>.yaml   (level: "user:<username>")
//
// Higher-specificity layers override common entries with the same name.
// Missing files are silently skipped. Within each layer, top-level
// `key: !include "file"` directives produce sub-levels like "user:rayben:ghostty".
func LoadAll(configDir, username, hostname string) (*Config, error) {
	type layer struct {
		path  string
		level string
	}
	layers := []layer{
		{filepath.Join(configDir, "default.yaml"), "common"},
		{filepath.Join(configDir, "hosts", hostname+".yaml"), "host:" + hostname},
		{filepath.Join(configDir, "users", username+".yaml"), "user:" + username},
	}

	merged := Config{Vars: make(map[string]string)}

	for _, l := range layers {
		cfg, err := loadLayerOptional(l.path, l.level, make(map[string]bool))
		if err != nil {
			return nil, fmt.Errorf("loading config layer %q: %w", l.path, err)
		}
		if cfg == nil {
			continue
		}
		merged = *mergeConfigs(&merged, cfg)
	}
	// Inject implicit variables. config_root is always available; user-defined
	// vars may reference it. ~ is expanded inline by ResolveString/resolveOne.
	absConfigDir, err := filepath.Abs(configDir)
	if err != nil {
		absConfigDir = configDir
	}
	merged.Vars["config_root"] = absConfigDir

	// Resolve variable references within var values (transitive), then apply
	// the fully-resolved vars to all string fields across the config.
	resolved := ResolveVars(merged.Vars)
	ApplyVars(&merged, resolved)
	// Expose the resolved vars so callers (e.g. --debug=variables) can inspect them.
	merged.Vars = resolved

	return &merged, nil
}

// NodeID returns the canonical resource node identifier used as a key in
// state and dependency references.
//
//	NodeID("packages", "neovim") → "packages/neovim"
func NodeID(resourceType, name string) string {
	return resourceType + "/" + name
}

// Hash computes a deterministic SHA-256 fingerprint of any config node value.
// The input is serialized to sorted-key JSON to guarantee key ordering,
// then hashed. The first 16 hex characters of the digest are returned.
func Hash(v any) string {
	// Marshal to JSON, then parse into a generic type to sort map keys.
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	// Re-encode with sorted keys via the generic sorted marshal path.
	sorted, err := sortedJSON(raw)
	if err != nil {
		// Fallback: use raw bytes.
		sorted = raw
	}
	sum := sha256.Sum256(sorted)
	return hex.EncodeToString(sum[:8]) // 16-char hex prefix is sufficient for change detection
}

// sortedJSON re-encodes raw JSON with map keys sorted to guarantee determinism.
func sortedJSON(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	v = sortValue(v)
	return json.Marshal(v)
}

// sortValue recursively sorts map keys in a generic JSON value tree.
func sortValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([][2]any, len(keys))
		for i, k := range keys {
			out[i] = [2]any{k, sortValue(t[k])}
		}
		return out
	case []any:
		for i, elem := range t {
			t[i] = sortValue(elem)
		}
		return t
	}
	return v
}
