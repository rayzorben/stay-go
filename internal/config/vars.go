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
	for i := range cfg.Files {
		// Source: ${secrets.*} refs are intentionally left untouched by resolveOne.
		cfg.Files[i].Source = r(cfg.Files[i].Source)
		cfg.Files[i].Target = r(cfg.Files[i].Target)
		for j := range cfg.Files[i].SSHKey {
			cfg.Files[i].SSHKey[j] = r(cfg.Files[i].SSHKey[j])
		}
		for j, dep := range cfg.Files[i].Depends {
			for k, vals := range dep {
				for l, v := range vals {
					cfg.Files[i].Depends[j][k][l] = r(v)
				}
			}
		}
	}
	for i := range cfg.Containers {
		cfg.Containers[i].Image = r(cfg.Containers[i].Image)
		cfg.Containers[i].Hostname = r(cfg.Containers[i].Hostname)
		cfg.Containers[i].Entrypoint = r(cfg.Containers[i].Entrypoint)
		for j := range cfg.Containers[i].Volumes {
			cfg.Containers[i].Volumes[j] = r(cfg.Containers[i].Volumes[j])
		}
		for j := range cfg.Containers[i].Environment {
			cfg.Containers[i].Environment[j] = r(cfg.Containers[i].Environment[j])
		}
		for j := range cfg.Containers[i].EnvFile {
			cfg.Containers[i].EnvFile[j] = r(cfg.Containers[i].EnvFile[j])
		}
	}
	for i := range cfg.Distrobox {
		cfg.Distrobox[i].Image = r(cfg.Distrobox[i].Image)
		cfg.Distrobox[i].Home = r(cfg.Distrobox[i].Home)
		for j := range cfg.Distrobox[i].Packages {
			cfg.Distrobox[i].Packages[j].Name = r(cfg.Distrobox[i].Packages[j].Name)
		}
		for j := range cfg.Distrobox[i].Commands {
			cfg.Distrobox[i].Commands[j].Command = r(cfg.Distrobox[i].Commands[j].Command)
			cfg.Distrobox[i].Commands[j].Rollback = r(cfg.Distrobox[i].Commands[j].Rollback)
		}
	}
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
// intentionally left untouched; they are substituted at execute time by
// config.ApplySecrets so secrets are never printed in plan output.
func resolveOne(s string, vars map[string]string, home string) string {
	s = strings.ReplaceAll(s, "~", home)
	s = resolveEnvVars(s)
	s = resolveCommandSubs(s)
	for k, v := range vars {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}
	return s
}

// hasSecretsRef reports whether s contains at least one ${secrets.*} token.
func hasSecretsRef(s string) bool {
	return strings.Contains(s, "${secrets.")
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
