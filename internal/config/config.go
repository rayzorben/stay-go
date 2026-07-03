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
	Vars       VarsMap           `yaml:"variables"`
	Packages   []PackageEntry    `yaml:"packages"`
	Groups     []GroupEntry      `yaml:"groups"`
	Users      []UserEntry       `yaml:"users"`
	Services   serviceList       `yaml:"services"`
	Scripts    []ScriptEntry     `yaml:"scripts"`
	Files      []FileEntry       `yaml:"files"`
	Commands   CommandList       `yaml:"commands"`
	Secrets    SecretsMap        `yaml:"secrets"`
	Containers ContainerList     `yaml:"containers"`
	Compose    []ComposeEntry    `yaml:"compose"`
	Flatpak    FlatpakConfig     `yaml:"flatpak"`
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
	Name       string                `yaml:"name"`
	Command    string                `yaml:"command"`
	Rollback   string                `yaml:"rollback,omitempty"`
	Sudo       bool                  `yaml:"sudo,omitempty"`
	Depends    []map[string][]string `yaml:"depends,omitempty"`
	SourceFile string                `yaml:"-" json:"-"` // relative path to source YAML, set by LoadAll
}

// DependsOnIDs converts the raw depends field into canonical resource node IDs,
// skipping the special "files" and "folders" keys which are handled at plan time
// (same pattern as ScriptEntry).
func (e *CommandEntry) DependsOnIDs() []string {
	var ids []string
	for _, dep := range e.Depends {
		for resourceType, names := range dep {
			if resourceType == "files" || resourceType == "folders" {
				continue
			}
			for _, name := range names {
				ids = append(ids, resourceType+"/"+name)
			}
		}
	}
	return ids
}

// FolderConditions returns the folder conditions declared in depends (see scripts).
// A path starting with "!" means the folder must NOT exist; otherwise it must exist.
func (e *CommandEntry) FolderConditions() []string {
	for _, dep := range e.Depends {
		if folders, ok := dep["folders"]; ok {
			return folders
		}
	}
	return nil
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
	// Source is a path, a ${secrets.x} reference, a git URL, or an http URL.
	// Tagged secrets:"-" because a ${secrets.x} value here is a structural marker
	// ("the file's content IS this secret"), not a value to substitute inline.
	Source  string `yaml:"source,omitempty" secrets:"-"`
	Content string `yaml:"content,omitempty"` // inline file content (alternative to source)
	// Add, when true with Content set, means: ensure Content appears in the target
	// file (append if missing); on removal from config, strip that snippet from the file.
	Add        bool                  `yaml:"add,omitempty"`
	Target     string                `yaml:"target"`            // destination path (~ expanded)
	Mode       string                `yaml:"mode,omitempty"`    // e.g. "0600", "+x"
	Symlink    bool                  `yaml:"symlink,omitempty"` // create symlink instead of copy (local only)
	SSHKey     []string              `yaml:"ssh_key,omitempty"` // SSH key paths for git SSH auth
	Sudo       bool                  `yaml:"sudo,omitempty"`
	Depends    []map[string][]string `yaml:"depends,omitempty"`
	SourceFile string                `yaml:"-" json:"-"` // relative path to source YAML, set by LoadAll
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
	Script     string                `yaml:"script"`
	Sudo       bool                  `yaml:"sudo,omitempty"`
	Depends    []map[string][]string `yaml:"depends,omitempty"`
	SourceFile string                `yaml:"-" json:"-"` // relative path to source YAML, set by LoadAll
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
	Name       string
	SourceFile string `yaml:"-" json:"-"` // relative path to source YAML, set by LoadAll
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
	Name       string
	Remove     bool
	Depends    []map[string][]string `yaml:"depends,omitempty"` // resource deps; must round-trip in YAML (e.g. distrobox guest config)
	Services   []ServiceEntry        `yaml:"-"`                  // mapping form only; cleared after normalization
	SourceFile string                `yaml:"-" json:"-"`         // relative path to source YAML, set by LoadAll
}

// DependsOnIDs converts the raw depends field into canonical resource node IDs.
func (p *PackageEntry) DependsOnIDs() []string {
	var ids []string
	for _, dep := range p.Depends {
		for resourceType, names := range dep {
			for _, name := range names {
				ids = append(ids, resourceType+"/"+name)
			}
		}
	}
	return ids
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
			Depends  []map[string][]string  `yaml:"depends,omitempty"`
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
		p.Depends = m.Depends
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
	Username   string   `yaml:"username"`
	Name       string   `yaml:"name,omitempty"` // full display name (GECOS)
	Shell      string   `yaml:"shell,omitempty"`
	Home       string   `yaml:"home,omitempty"`
	UID        string   `yaml:"uid,omitempty"`
	Groups     []string `yaml:"groups,omitempty"`
	SourceFile string   `yaml:"-" json:"-"` // relative path to source YAML, set by LoadAll
}

// ServiceEntry defines the desired state of a systemd service.
type ServiceEntry struct {
	Service    string                `yaml:"service"`
	User       bool                  `yaml:"user,omitempty"`    // true = systemd user service
	Enabled    *bool                 `yaml:"enabled,omitempty"` // default true
	Now        *bool                 `yaml:"now,omitempty"`     // default true; false = enable without starting
	Depends    []map[string][]string `yaml:"depends,omitempty"` // [{packages: [docker]}]
	SourceFile string                `yaml:"-" json:"-"`        // relative path to source YAML, set by LoadAll
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
// Field names mirror docker-compose conventions; see ContainerList and the
// flexible field types (envVars, Mount) for the compose forms also accepted.
type ContainerEntry struct {
	Name        string                `yaml:"name,omitempty"` // also accepts compose "container_name" (see UnmarshalYAML)
	Image       string                `yaml:"image"`
	Runtime     string                `yaml:"runtime,omitempty"` // docker or podman; auto-detected if empty
	Ports       []string              `yaml:"ports,omitempty"`   // "hostPort:containerPort[/proto]"
	Volumes     []Mount               `yaml:"volumes,omitempty"` // short "host:container[:opts]" or compose long form
	Environment envVars               `yaml:"environment,omitempty"` // ["KEY=value"] list or {KEY: value} mapping
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
	ShmSize     string                `yaml:"shm_size,omitempty"`          // e.g. "512mb"
	StopTimeout string                `yaml:"stop_grace_period,omitempty"` // duration ("30s") or seconds ("30")
	Pull        string                `yaml:"pull,omitempty"` // always, missing (default), never
	Sudo        bool                  `yaml:"sudo,omitempty"`
	Depends     []map[string][]string `yaml:"depends,omitempty"`
	SourceFile  string                `yaml:"-" json:"-"` // relative path to source YAML, set by LoadAll
}

// UnmarshalYAML decodes a container entry, accepting the docker-compose alias
// "container_name" for "name". All other fields decode via their struct tags
// (the flexible types envVars and Mount handle the remaining compose forms).
func (c *ContainerEntry) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("container entry: expected mapping, got kind %v", value.Kind)
	}
	type plain ContainerEntry // no UnmarshalYAML method → decodes via struct tags
	var p plain
	if err := value.Decode(&p); err != nil {
		return fmt.Errorf("decoding container entry: %w", err)
	}
	*c = ContainerEntry(p)
	if c.Name == "" {
		var alias struct {
			ContainerName string `yaml:"container_name"`
		}
		if value.Decode(&alias) == nil {
			c.Name = alias.ContainerName
		}
	}
	return nil
}

// ContainerList is the containers: section. It accepts either a sequence of
// container mappings (stay-go's native form) or a mapping keyed by container
// name (docker-compose's services: form). In the mapping form the key supplies
// the container name unless the entry itself sets name/container_name.
type ContainerList []ContainerEntry

// UnmarshalYAML implements yaml.Unmarshaler for the two accepted shapes.
func (cl *ContainerList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		var out []ContainerEntry
		if err := value.Decode(&out); err != nil {
			return fmt.Errorf("containers: %w", err)
		}
		*cl = out
	case yaml.MappingNode:
		var out []ContainerEntry
		for i := 0; i+1 < len(value.Content); i += 2 {
			name := value.Content[i].Value
			var e ContainerEntry
			if err := value.Content[i+1].Decode(&e); err != nil {
				return fmt.Errorf("containers/%s: %w", name, err)
			}
			if e.Name == "" {
				e.Name = name
			}
			out = append(out, e)
		}
		*cl = out
	default:
		return fmt.Errorf("containers: expected sequence or mapping")
	}
	return nil
}

// envVars holds container environment variables. It accepts both the list form
// (["KEY=value", ...]) and the docker-compose mapping form ({KEY: value, ...}),
// normalising either to a deterministic, order-preserving []string of "KEY=value".
// A null mapping value (compose "KEY:" with no value) becomes a bare "KEY",
// meaning "inherit from the host environment".
type envVars []string

// UnmarshalYAML implements yaml.Unmarshaler.
func (e *envVars) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return fmt.Errorf("environment: %w", err)
		}
		*e = list
	case yaml.MappingNode:
		list := make([]string, 0, len(value.Content)/2)
		for i := 0; i+1 < len(value.Content); i += 2 {
			key := value.Content[i].Value
			var v interface{}
			if err := value.Content[i+1].Decode(&v); err != nil {
				return fmt.Errorf("environment[%s]: %w", key, err)
			}
			if v == nil {
				list = append(list, key)
			} else {
				list = append(list, fmt.Sprintf("%s=%v", key, v))
			}
		}
		*e = list
	default:
		return fmt.Errorf("environment: expected sequence or mapping")
	}
	return nil
}

// Mount is one entry under a container's volumes:. It accepts the short string
// form ("host:container[:options]" — stored verbatim in Short) and the
// docker-compose long form (a mapping with type/source/target/read_only and,
// for tmpfs mounts, tmpfs.size). The containers resource renders it to the
// appropriate `docker run` flag (-v or --tmpfs).
type Mount struct {
	// Short is the short "src:tgt[:opts]" form, used verbatim. It is populated by
	// UnmarshalYAML (not by struct-tag decoding) and excluded from re-marshalling
	// via MarshalYAML; it deliberately has no yaml:"-" tag so that the config
	// substitution pipeline still resolves ${var}/${secrets.x} inside it.
	Short     string `json:"short,omitempty"`
	Type      string `yaml:"type,omitempty" json:"type,omitempty"` // bind | volume | tmpfs
	Source    string `yaml:"source,omitempty" json:"source,omitempty"`
	Target    string `yaml:"target,omitempty" json:"target,omitempty"`
	ReadOnly  bool   `yaml:"read_only,omitempty" json:"read_only,omitempty"`
	TmpfsSize string `json:"tmpfs_size,omitempty"` // tmpfs.size, normalised to a string (populated by UnmarshalYAML)
}

// MarshalYAML keeps Mount round-tripping cleanly: the short form marshals back
// to a scalar, the long form to a mapping.
func (m Mount) MarshalYAML() (interface{}, error) {
	if m.Short != "" {
		return m.Short, nil
	}
	out := map[string]interface{}{}
	if m.Type != "" {
		out["type"] = m.Type
	}
	if m.Source != "" {
		out["source"] = m.Source
	}
	if m.Target != "" {
		out["target"] = m.Target
	}
	if m.ReadOnly {
		out["read_only"] = true
	}
	if m.TmpfsSize != "" {
		out["tmpfs"] = map[string]interface{}{"size": m.TmpfsSize}
	}
	return out, nil
}

// UnmarshalYAML implements yaml.Unmarshaler for the short and long mount forms.
func (m *Mount) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		m.Short = value.Value
		return nil
	case yaml.MappingNode:
		var raw struct {
			Type     string `yaml:"type"`
			Source   string `yaml:"source"`
			Target   string `yaml:"target"`
			ReadOnly bool   `yaml:"read_only"`
			Tmpfs    struct {
				Size interface{} `yaml:"size"`
			} `yaml:"tmpfs"`
		}
		if err := value.Decode(&raw); err != nil {
			return fmt.Errorf("decoding volume mount: %w", err)
		}
		m.Type, m.Source, m.Target, m.ReadOnly = raw.Type, raw.Source, raw.Target, raw.ReadOnly
		if raw.Tmpfs.Size != nil {
			m.TmpfsSize = fmt.Sprintf("%v", raw.Tmpfs.Size)
		}
		return nil
	default:
		return fmt.Errorf("volume mount: expected string or mapping, got kind %v", value.Kind)
	}
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

// ComposeEntry declares a docker-compose project managed by stay-go: a directory
// of compose files brought up with `docker compose up -d` (and torn down with
// `docker compose down`).
//
// Before each up/down the project directory is rendered into a private cache
// directory with the same substitution applied to every file's contents as to
// any config string field — ${var}, ${env:VAR}, $(cmd), ~, and ${secrets.x} —
// so the source files on disk are never modified. The rendered .env therefore
// holds the plaintext secret, but the cache directory is owner-only (0700/0600).
//
// Because the project directory is the rendered copy, bind-mount paths inside
// the compose files must be absolute (or built from ${config_root}); relative
// paths would resolve against the rendered copy, not the original directory.
type ComposeEntry struct {
	Project    string                `yaml:"project,omitempty"`  // compose project name (-p); defaults to the basename of Path
	Path       string                `yaml:"path"`               // required — directory containing the compose files
	Files      []string              `yaml:"files,omitempty"`    // compose files relative to Path; default: first standard name (+ .override) found
	EnvFile    string                `yaml:"env_file,omitempty"` // env file relative to Path passed via --env-file (only needed for a non-".env" name)
	Runtime    string                `yaml:"runtime,omitempty"`  // "docker" or "podman"; auto-detected if empty
	Sudo       bool                  `yaml:"sudo,omitempty"`
	Depends    []map[string][]string `yaml:"depends,omitempty"`
	SourceFile string                `yaml:"-" json:"-"` // relative path to source YAML, set by LoadAll
}

// ProjectName returns the effective compose project name: the explicit project
// field, or the basename of Path.
func (c *ComposeEntry) ProjectName() string {
	if c.Project != "" {
		return c.Project
	}
	return filepath.Base(c.Path)
}

// DependsOnIDs converts the raw depends field into canonical resource node IDs.
func (c *ComposeEntry) DependsOnIDs() []string {
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
	Root     bool                  `yaml:"root,omitempty"`      // pass --root to distrobox create (rootful container manager); default false
	Extends  string                `yaml:"extends,omitempty"`   // path to a base file; merged under entry-specific fields
	// Packages and Commands are serialised verbatim into the in-box stay-go's
	// config; tagged secrets:"-" so any ${secrets.x} they contain stays a token
	// (resolved, if at all, by the guest) rather than being baked into the
	// guest config file as plaintext.
	Packages []PackageEntry        `yaml:"packages,omitempty" secrets:"-"`  // in-box packages
	Commands []CommandEntry        `yaml:"commands,omitempty" secrets:"-"`  // in-box inline commands
	Exports    []string              `yaml:"exports,omitempty"`   // app names to export via distrobox-export
	Depends    []map[string][]string `yaml:"depends,omitempty"`   // host-level deps (e.g. services: [docker])
	SourceFile string                `yaml:"-" json:"-"`          // relative path to source YAML, set by LoadAll
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

// FlatpakConfig holds all Flatpak-managed remotes and applications.
type FlatpakConfig struct {
	Remotes []FlatpakRemoteEntry `yaml:"remotes,omitempty"`
	Apps    []FlatpakAppEntry    `yaml:"apps,omitempty"`
}

// FlatpakRemoteEntry defines a Flatpak remote repository to manage.
type FlatpakRemoteEntry struct {
	Name       string `yaml:"name"`
	URL        string `yaml:"url"`
	SourceFile string `yaml:"-" json:"-"` // relative path to source YAML, set by LoadAll
}

// FlatpakAppEntry defines a Flatpak application to manage.
type FlatpakAppEntry struct {
	AppID      string                `yaml:"app"`
	Remote     string                `yaml:"remote,omitempty"` // defaults to "flathub"
	Depends    []map[string][]string `yaml:"depends,omitempty"`
	SourceFile string                `yaml:"-" json:"-"` // relative path to source YAML, set by LoadAll
}

// DependsOnIDs converts the raw depends field into canonical resource node IDs.
func (a *FlatpakAppEntry) DependsOnIDs() []string {
	var ids []string
	for _, dep := range a.Depends {
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
	File       string                 `yaml:"file"`
	Values     map[string]interface{} `yaml:"-"` // populated by UnmarshalYAML
	Depends    []map[string][]string  `yaml:"depends,omitempty"`
	SourceFile string                 `yaml:"-" json:"-"` // relative path to source YAML, set by LoadAll
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
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	configRoot := filepath.Dir(absPath)
	cfg, err := loadLayer(absPath, configRoot, make(map[string]bool))
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
// Each entry's SourceFile is set to the path of the YAML file it came from,
// relative to the config root (e.g. "/default.yaml", "/users/rayben.yaml").
func LoadAll(configDir string) (*Config, error) {
	absConfigDir, err := filepath.Abs(configDir)
	if err != nil {
		absConfigDir = configDir
	}

	cfg, err := loadLayerOptional(filepath.Join(configDir, "default.yaml"), absConfigDir, make(map[string]bool))
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

	if err := applyDistroboxExtends(cfg, resolved); err != nil {
		return nil, fmt.Errorf("distrobox extends: %w", err)
	}

	// Replace every ${secrets.x} token with the secret's ciphertext. After this
	// no config string contains a secret token or any plaintext; the decrypted
	// values are substituted later, just before execution, by the secrets
	// resource (see resolve.go). Run last so it also covers distrobox-extended
	// and expanded entries.
	ResolveSecretsToCiphertext(cfg)

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
