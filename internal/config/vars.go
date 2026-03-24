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
	"strings"
)

const maxVarPasses = 15

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

// ApplyVars substitutes all ${key} and ~ tokens in every string field of cfg.
func ApplyVars(cfg *Config, vars map[string]string) {
	r := func(s string) string { return ResolveString(s, vars) }

	for i := range cfg.Packages {
		cfg.Packages[i].Name = r(cfg.Packages[i].Name)
	}
	for i := range cfg.Groups {
		cfg.Groups[i].Name = r(cfg.Groups[i].Name)
	}
	for i := range cfg.Users {
		cfg.Users[i].Shell = r(cfg.Users[i].Shell)
		cfg.Users[i].Home = r(cfg.Users[i].Home)
		cfg.Users[i].UID = r(cfg.Users[i].UID)
		for j, g := range cfg.Users[i].Groups {
			cfg.Users[i].Groups[j] = r(g)
		}
	}
	for i := range cfg.Services {
		cfg.Services[i].Service = r(cfg.Services[i].Service)
	}
	for i := range cfg.Scripts {
		cfg.Scripts[i].Script = r(cfg.Scripts[i].Script)
	}
	for i := range cfg.Commands {
		cfg.Commands[i].Command = r(cfg.Commands[i].Command)
		cfg.Commands[i].Rollback = r(cfg.Commands[i].Rollback)
		for j, dep := range cfg.Commands[i].Depends {
			for k, vals := range dep {
				for l, v := range vals {
					cfg.Commands[i].Depends[j][k][l] = r(v)
				}
			}
		}
	}
}

// resolveOne performs one substitution pass: expands ~ then replaces ${key}.
func resolveOne(s string, vars map[string]string, home string) string {
	s = strings.ReplaceAll(s, "~", home)
	for k, v := range vars {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}
	return s
}

// countVarTokens counts unresolved ${...} and ~ tokens across all var values.
// Used to detect when iterative resolution has stabilised.
func countVarTokens(vars map[string]string) int {
	n := 0
	for _, v := range vars {
		n += strings.Count(v, "${")
		n += strings.Count(v, "~")
	}
	return n
}
