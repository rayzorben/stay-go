// Package config — variable resolution.
//
// Variables are defined in the config YAML under the "vars:" key.
// They are referenced in any string field as ${var_name}.
// Two implicit variables are always available:
//
//	config_root — the config directory path passed to LoadAll
//	             (e.g. "config" if invoked with --config config)
//
// The tilde (~) is treated as a shorthand for the current user's home
// directory and is expanded wherever it appears in any string value.
//
// Resolution is iterative: after each pass the number of unresolved tokens
// (${...} and ~) is counted. If the count did not change, or maxVarPasses
// is reached, resolution stops. This handles transitive references up to
// that depth without infinite loops.
package config

import (
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxVarPasses = 15

// VarsMap is the in-memory representation of a config variables: block.
// Keys are dot-joined paths, supporting both flat and nested YAML:
//
//	Flat:   key: value              → key "key"
//	Nested: group:\n  key: value   → key "group.key"
//
// Both forms may coexist within the same variables: block.
type VarsMap map[string]string

// UnmarshalYAML implements yaml.Unmarshaler. Scalar leaves are stored directly;
// plain mapping nodes are recursed with a dot-joined prefix.
func (m *VarsMap) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return nil
	}
	*m = make(VarsMap)
	return unmarshalVarsGroup(value, "", *m)
}

// unmarshalVarsGroup recursively walks a variables mapping node, flattening
// nested groups into dot-joined keys in out. prefix is empty at the top level.
func unmarshalVarsGroup(node *yaml.Node, prefix string, out VarsMap) error {
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]

		key := keyNode.Value
		if prefix != "" {
			key = prefix + "." + keyNode.Value
		}

		// A plain mapping is a nested group — recurse.
		if valNode.Kind == yaml.MappingNode {
			if err := unmarshalVarsGroup(valNode, key, out); err != nil {
				return err
			}
			continue
		}

		out[key] = valNode.Value
	}
	return nil
}

// ResolveVars resolves all variable references within the vars map itself
// (e.g. vars that reference other vars). Returns a new map with fully
// resolved values. The caller is responsible for injecting implicit vars
// (config_root) before calling this function.
func ResolveVars(vars map[string]string) map[string]string {
	home, _ := os.UserHomeDir()

	resolved := make(map[string]string, len(vars))
	for k, v := range vars {
		resolved[k] = v
	}

	for i := 0; i < maxVarPasses; i++ {
		prev := countVarTokens(resolved)
		for k, v := range resolved {
			resolved[k] = resolveOne(v, resolved, home)
		}
		if countVarTokens(resolved) == prev {
			break
		}
	}
	return resolved
}

// ResolveString applies the resolved vars map and ~ expansion to a single
// string. Used by ApplyVars and anywhere else a single field needs resolution.
func ResolveString(s string, vars map[string]string) string {
	home, _ := os.UserHomeDir()
	return resolveOne(s, vars, home)
}

// ApplyVars substitutes all ${key}, ${env:VAR}, $(cmd), and ~ tokens in every
// string field of cfg by walking the entire value tree via reflection. Fields
// tagged `yaml:"-"` are skipped (internal-only; must not be mutated here).
// ${secrets.x} tokens are intentionally left for the secrets pipeline (see
// resolve.go) — ResolveString does not touch them.
func ApplyVars(cfg *Config, vars map[string]string) {
	walkConfigStrings(cfg, skipInternal, func(s string) string { return ResolveString(s, vars) })
}

// resolveEnvVars replaces ${env:NAME} tokens with the corresponding OS
// environment variable value. Missing variables expand to empty string.
func resolveEnvVars(s string) string {
	const prefix = "${env:"
	for {
		start := strings.Index(s, prefix)
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], "}")
		if end < 0 {
			break
		}
		end += start
		name := s[start+len(prefix) : end]
		s = s[:start] + os.Getenv(name) + s[end+1:]
	}
	return s
}

// resolveCommandSubs replaces $(command) tokens with the trimmed stdout of
// running the command via bash. On error the token expands to empty string.
func resolveCommandSubs(s string) string {
	for {
		start := strings.Index(s, "$(")
		if start < 0 {
			break
		}
		depth, end := 0, -1
		for i := start + 2; i < len(s); i++ {
			switch s[i] {
			case '(':
				depth++
			case ')':
				if depth == 0 {
					end = i
				} else {
					depth--
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			break
		}
		cmd := s[start+2 : end]
		out, err := exec.Command("bash", "-c", cmd).Output()
		result := ""
		if err == nil {
			result = strings.TrimSpace(string(out))
		}
		s = s[:start] + result + s[end+1:]
	}
	return s
}

// resolveOne performs one substitution pass: expands ~, ${env:VAR}, $(cmd),
// then replaces ${key} from vars. Tokens of the form ${secrets.*} are
// intentionally left untouched; they are resolved by the secrets pipeline
// (ResolveSecretsToCiphertext at load time, then the secrets resource at
// execute time — see resolve.go) so secrets never appear in plan output.
func resolveOne(s string, vars map[string]string, home string) string {
	s = strings.ReplaceAll(s, "~", home)
	s = resolveEnvVars(s)
	s = resolveCommandSubs(s)
	for k, v := range vars {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}
	return s
}

// countVarTokens counts unresolved ${...}, ~, and $(...) tokens across all
// var values. Used to detect when iterative resolution has stabilised.
func countVarTokens(vars map[string]string) int {
	n := 0
	for _, v := range vars {
		n += strings.Count(v, "${")
		n += strings.Count(v, "~")
		n += strings.Count(v, "$(")
	}
	return n
}
