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
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level structure for the desired system state.
type Config struct {
	Vars       map[string]string `yaml:"variables"`
	Packages   []PackageEntry    `yaml:"packages"`
	Groups     []GroupEntry      `yaml:"groups"`
	Users      []UserEntry       `yaml:"users"`
	Services   serviceList       `yaml:"services"`
	Scripts    []ScriptEntry     `yaml:"scripts"`
	Files      []FileEntry       `yaml:"files"`
	Commands   CommandList       `yaml:"commands"`
	Secrets    SecretsMap        `yaml:"secrets"`
	Containers []ContainerEntry  `yaml:"containers"`
	Distrobox  []DistroboxEntry  `yaml:"distrobox"`

	Json []JsonEntry `yaml:"json"`

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

// CommandList is the commands: sequence, accepting either a shorthand string
// ("sh foo.sh") or a full mapping. A string becomes both Name and Command.
type CommandList []CommandEntry

// UnmarshalYAML decodes each element as a scalar (name+command) or mapping.
func (c *CommandList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("commands: expected sequence")
	}
	var out []CommandEntry
	for _, n := range value.Content {
		switch n.Kind {
		case yaml.ScalarNode:
			cmd := strings.TrimSpace(n.Value)
			if cmd == "" {
				continue
			}
			out = append(out, CommandEntry{Name: cmd, Command: cmd})
		case yaml.MappingNode:
			var e CommandEntry
			if err := n.Decode(&e); err != nil {
				return fmt.Errorf("commands: %w", err)
			}
			if e.Name == "" && e.Command != "" {
				e.Name = e.Command
			}
			if e.Command == "" && e.Name != "" {
				e.Command = e.Name
			}
			if e.Name == "" {
				return fmt.Errorf("commands: mapping needs name or command")
			}
			out = append(out, e)
		default:
			return fmt.Errorf("commands: each entry must be a string or mapping")
		}
	}
	*c = out
	return nil
}

// serviceList is the services: sequence: each item may be a service name string
// or a full ServiceEntry mapping.
type serviceList []ServiceEntry

// UnmarshalYAML decodes each element as a scalar (service name) or mapping.
func (s *serviceList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("services: expected sequence")
	}
	var out []ServiceEntry
	for _, n := range value.Content {
		switch n.Kind {
		case yaml.ScalarNode:
			name := strings.TrimSpace(n.Value)
			if name == "" {
				continue
			}
			out = append(out, ServiceEntry{Service: name})
		case yaml.MappingNode:
			var e ServiceEntry
			if err := n.Decode(&e); err != nil {
				return fmt.Errorf("services: %w", err)
			}
			if e.Service == "" {
				return fmt.Errorf("services: mapping needs \"service\"")
			}
			out = append(out, e)
		default:
			return fmt.Errorf("services: each entry must be a string or mapping")
		}
	}
	*s = out
	return nil
}

// FileEntry defines a file, directory clone, or download to place on disk.
type FileEntry struct {
	Source  string `yaml:"source,omitempty"`  // path, ${secrets.*}, git URL, or http URL
	Content string `yaml:"content,omitempty"` // inline file content (alternative to source)
	// Add, when true with Content set, means: ensure Content appears in the target
	// file (append if missing); on removal from config, strip that snippet from the file.
	Add     bool                  `yaml:"add,omitempty"`
	Target  string                `yaml:"target"`            // destination path (~ expanded)
	Mode    string                `yaml:"mode,omitempty"`    // e.g. "0600", "+x"
	Symlink bool                  `yaml:"symlink,omitempty"` // create symlink instead of copy (local only)
	SSHKey  []string              `yaml:"ssh_key,omitempty"` // SSH key paths for git SSH auth
	Sudo    bool                  `yaml:"sudo,omitempty"`
	Depends []map[string][]string `yaml:"depends,omitempty"`
	Level   string                `yaml:"-"` // set by LoadAll, not parsed from YAML
}

// DependsOnIDs converts the raw depends field into canonical resource node IDs.
// The special "file" key references other file entries by their target path.
func (e *FileEntry) DependsOnIDs() []string {
	var ids []string
	for _, dep := range e.Depends {
		for resourceType, names := range dep {
			if resourceType == "file" {
				for _, name := range names {
					ids = append(ids, "files/"+name)
				}
				continue
			}
			for _, name := range names {
				ids = append(ids, resourceType+"/"+name)
			}
		}
	}
	return ids
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
// ("neovim") and mapping ({ name: neovim } or { package: docker }) forms in YAML.
// A leading "!" (e.g. "!foo") marks the package for forced removal.
//
// Optional services: each entry is expanded into top-level Services with an
// implicit depends on this package (see NormalizeExpandedForms).
type PackageEntry struct {
	Name     string
	Remove   bool
	Services []ServiceEntry `yaml:"-"` // mapping form only; cleared after normalization
	Level    string         `yaml:"-"` // set by LoadAll, not parsed from YAML
}

// inlinePackageService decodes a service under packages: as a string or full object.
type inlinePackageService ServiceEntry

func (s *inlinePackageService) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*s = inlinePackageService(ServiceEntry{Service: strings.TrimSpace(value.Value)})
		return nil
	case yaml.MappingNode:
		var e ServiceEntry
		if err := value.Decode(&e); err != nil {
			return fmt.Errorf("decoding package service: %w", err)
		}
		*s = inlinePackageService(e)
		return nil
	default:
		return fmt.Errorf("package service: expected scalar or mapping, got kind %v", value.Kind)
	}
}

// UnmarshalYAML implements yaml.Unmarshaler to accept both scalar and map forms.
func (p *PackageEntry) UnmarshalYAML(value *yaml.Node) error {
	applyName := func(raw string) {
		if strings.HasPrefix(raw, "!") {
			p.Name = raw[1:]
			p.Remove = true
		} else {
			p.Name = raw
		}
	}
	switch value.Kind {
	case yaml.ScalarNode:
		applyName(value.Value)
		return nil
	case yaml.MappingNode:
		var m struct {
			Name     string                 `yaml:"name"`
			Package  string                 `yaml:"package"`
			Services []inlinePackageService `yaml:"services,omitempty"`
		}
		if err := value.Decode(&m); err != nil {
			return fmt.Errorf("decoding package entry: %w", err)
		}
		raw := m.Package
		if raw == "" {
			raw = m.Name
		}
		if raw == "" {
			return fmt.Errorf("package entry: need \"name\" or \"package\"")
		}
		applyName(raw)
		if len(m.Services) > 0 {
			p.Services = make([]ServiceEntry, len(m.Services))
			for i := range m.Services {
				p.Services[i] = ServiceEntry(m.Services[i])
			}
		}
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
	Now     *bool                 `yaml:"now,omitempty"`     // default true; false = enable without starting
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

// IsNow returns whether the service should be started immediately on enable (defaults to true).
func (s *ServiceEntry) IsNow() bool {
	if s.Now == nil {
		return true
	}
	return *s.Now
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

// ContainerEntry defines the desired state of a container managed by Docker or Podman.
// Field names mirror docker-compose conventions.
type ContainerEntry struct {
	Name        string                `yaml:"name,omitempty"`
	Image       string                `yaml:"image"`
	Runtime     string                `yaml:"runtime,omitempty"`     // docker or podman; auto-detected if empty
	Ports       []string              `yaml:"ports,omitempty"`       // "hostPort:containerPort"
	Volumes     []string              `yaml:"volumes,omitempty"`     // "host:container[:options]"
	Environment []string              `yaml:"environment,omitempty"` // "KEY=value"
	EnvFile     []string              `yaml:"env_file,omitempty"`    // paths to env files
	Labels      map[string]string     `yaml:"labels,omitempty"`
	Restart     string                `yaml:"restart,omitempty"`      // no, always, unless-stopped, on-failure
	NetworkMode string                `yaml:"network_mode,omitempty"` // host, bridge, none, container:<name>
	Networks    []string              `yaml:"networks,omitempty"`
	Command     []string              `yaml:"command,omitempty"`
	Entrypoint  string                `yaml:"entrypoint,omitempty"`
	User        string                `yaml:"user,omitempty"`
	Privileged  bool                  `yaml:"privileged,omitempty"`
	CapAdd      []string              `yaml:"cap_add,omitempty"`
	CapDrop     []string              `yaml:"cap_drop,omitempty"`
	Devices     []string              `yaml:"devices,omitempty"`
	DNS         []string              `yaml:"dns,omitempty"`
	ExtraHosts  []string              `yaml:"extra_hosts,omitempty"` // "hostname:ip"
	Hostname    string                `yaml:"hostname,omitempty"`
	Pull        string                `yaml:"pull,omitempty"` // always, missing (default), never
	Sudo        bool                  `yaml:"sudo,omitempty"`
	Depends     []map[string][]string `yaml:"depends,omitempty"`
	Level       string                `yaml:"-"` // set by LoadAll, not parsed from YAML
}

// ContainerName returns the effective container name: the explicit name field,
// or derived from the image (last path segment, tag stripped).
func (c *ContainerEntry) ContainerName() string {
	if c.Name != "" {
		return c.Name
	}
	img := c.Image
	// Strip digest
	if i := strings.Index(img, "@"); i >= 0 {
		img = img[:i]
	}
	// Strip tag (but only after the last "/", to avoid stripping registry port)
	if i := strings.LastIndex(img, ":"); i >= 0 {
		if j := strings.LastIndex(img, "/"); j < 0 || i > j {
			img = img[:i]
		}
	}
	// Last path segment
	if i := strings.LastIndex(img, "/"); i >= 0 {
		img = img[i+1:]
	}
	return img
}

// DependsOnIDs converts the raw depends field into canonical resource node IDs.
func (c *ContainerEntry) DependsOnIDs() []string {
	var ids []string
	for _, dep := range c.Depends {
		for resourceType, names := range dep {
			for _, name := range names {
				ids = append(ids, resourceType+"/"+name)
			}
		}
	}
	return ids
}

// DistroboxEntry defines the desired state of a distrobox container and
// the resources managed inside it. The host manages the container lifecycle;
// in-box packages and commands are applied by a guest stay-go invocation.
type DistroboxEntry struct {
	Name     string                `yaml:"name"`
	Image    string                `yaml:"image"`
	Init     bool                  `yaml:"init,omitempty"`      // run /sbin/init (systemd inside)
	Home     string                `yaml:"home,omitempty"`      // custom home directory for the box
	HomeSudo bool                  `yaml:"home_sudo,omitempty"` // sudo required to create Home
	Packages []PackageEntry        `yaml:"packages,omitempty"`  // in-box packages
	Commands []CommandEntry        `yaml:"commands,omitempty"`  // in-box inline commands
	Exports  []string              `yaml:"exports,omitempty"`   // app names to export via distrobox-export
	Depends  []map[string][]string `yaml:"depends,omitempty"`   // host-level deps (e.g. services: [docker])
	Level    string                `yaml:"-"`                   // set by LoadAll, not parsed from YAML
}

// DependsOnIDs converts the raw depends field into canonical resource node IDs.
func (d *DistroboxEntry) DependsOnIDs() []string {
	var ids []string
	for _, dep := range d.Depends {
		for resourceType, names := range dep {
			for _, name := range names {
				ids = append(ids, resourceType+"/"+name)
			}
		}
	}
	return ids
}

// JsonEntry defines desired values to set at specific JSON paths within a file.
// The File field is the path to the JSON file to manage. Values is a map of
// JSON paths (using gron-style dot/bracket notation, e.g. "json.foo[0].bar")
// to their desired values. The original values are saved to state on first
// application and restored on removal.
type JsonEntry struct {
	File    string                 `yaml:"file"`
	Values  map[string]interface{} `yaml:"-"` // populated by UnmarshalYAML
	Depends []map[string][]string  `yaml:"depends,omitempty"`
	Level   string                 `yaml:"-"` // set by LoadAll, not parsed from YAML
}

// UnmarshalYAML parses a json entry. The mapping may contain "file",
// "depends", and any number of json-path keys (prefixed with "json.").
func (e *JsonEntry) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("json entry must be a mapping")
	}
	e.Values = make(map[string]interface{})
	for i := 0; i+1 < len(value.Content); i += 2 {
		k := value.Content[i].Value
		v := value.Content[i+1]
		switch k {
		case "file":
			e.File = v.Value
		case "depends":
			if err := v.Decode(&e.Depends); err != nil {
				return fmt.Errorf("decoding json depends: %w", err)
			}
		default:
			// All other keys are json-path assignments.
			// If the value is a mapping, flatten it into individual full paths
			// (e.g. "json.barConfigs[0]" + {borderEnabled: true} →
			//  "json.barConfigs[0].borderEnabled": true).
			// This ensures only the specified leaf values are touched; other
			// properties at the same level are left unchanged.
			// Scalar values are stored directly (e.g. "json.foo.bar": true).
			flattenYAMLNode(k, v, e.Values)
		}
	}
	return nil
}

// flattenYAMLNode recursively flattens a yaml.Node into the out map under prefix.
// Mapping nodes produce "prefix.key" entries; sequence nodes produce "prefix[i]"
// entries; scalar nodes store the decoded value directly at prefix.
func flattenYAMLNode(prefix string, node *yaml.Node, out map[string]interface{}) {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			childKey := node.Content[i].Value
			childVal := node.Content[i+1]
			flattenYAMLNode(prefix+"."+childKey, childVal, out)
		}
	case yaml.SequenceNode:
		for i, elem := range node.Content {
			flattenYAMLNode(fmt.Sprintf("%s[%d]", prefix, i), elem, out)
		}
	default:
		var val interface{}
		if err := node.Decode(&val); err == nil {
			out[prefix] = val
		}
	}
}

// DependsOnIDs converts the raw depends field into canonical resource node IDs.
func (e *JsonEntry) DependsOnIDs() []string {
	var ids []string
	for _, dep := range e.Depends {
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
	cfg, err := loadLayer(path, "", make(map[string]bool))
	if err != nil {
		return nil, err
	}
	NormalizeExpandedForms(cfg)
	return cfg, nil
}

// LoadAll loads config from default.yaml in configDir. Host- and user-specific
// layers are included via `key: !include "path"` directives in that file.
// Include paths support ${config_root}, ${env:VAR}, $(command), and ~.
//
// Direct entries in default.yaml carry level "common". Included files inherit
// their level from the key name, e.g. `"user:rayben": !include "..."` produces
// level "user:rayben", matching the old explicit-layer naming.
func LoadAll(configDir string) (*Config, error) {
	absConfigDir, err := filepath.Abs(configDir)
	if err != nil {
		absConfigDir = configDir
	}

	// Load default.yaml with level "" — stampLevel maps this to "common" so
	// that direct entries remain backward-compatible with existing state.
	cfg, err := loadLayerOptional(filepath.Join(configDir, "default.yaml"), "", make(map[string]bool))
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if cfg == nil {
		cfg = &Config{Vars: make(map[string]string)}
	}

	// Inject config_root so var values and remaining include paths can reference it.
	cfg.Vars["config_root"] = absConfigDir

	// Resolve variable references within var values (transitive), then apply
	// the fully-resolved vars to all string fields across the config.
	resolved := ResolveVars(cfg.Vars)
	ApplyVars(cfg, resolved)
	NormalizeExpandedForms(cfg)
	cfg.Vars = resolved

	return cfg, nil
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
