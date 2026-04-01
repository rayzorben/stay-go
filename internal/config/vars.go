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
	"reflect"
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

// ApplyVars substitutes all ${key} and ~ tokens in every string field of cfg
// by walking the entire value tree via reflection. Fields tagged `yaml:"-"` are
// skipped (they are internal-only and must not be mutated here).
func ApplyVars(cfg *Config, vars map[string]string) {
	r := func(s string) string { return ResolveString(s, vars) }
	applyVarsValue(reflect.ValueOf(cfg), r)
}

// applyVarsValue recursively walks v and calls r on every settable string.
// Pointers and interfaces are followed. Structs skip fields tagged yaml:"-".
// map[string][]string (the Depends shape) has its values resolved in place.
func applyVarsValue(v reflect.Value, r func(string) string) {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if !v.IsNil() {
			applyVarsValue(v.Elem(), r)
		}
	case reflect.Struct:
		t := v.Type()
		for i := range v.NumField() {
			ft := t.Field(i)
			// Skip fields marked yaml:"-" — these are internal (Level, DecryptedSecrets, etc.)
			if ft.Tag.Get("yaml") == "-" {
				continue
			}
			applyVarsValue(v.Field(i), r)
		}
	case reflect.Slice:
		for i := range v.Len() {
			applyVarsValue(v.Index(i), r)
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			elem := v.MapIndex(key)
			resolved := applyVarsMapValue(elem, r)
			if resolved.IsValid() {
				v.SetMapIndex(key, resolved)
			}
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(r(v.String()))
		}
	}
}

// applyVarsMapValue resolves variables in a map value (which is not addressable
// and therefore not directly settable). Returns the new reflect.Value to set.
func applyVarsMapValue(v reflect.Value, r func(string) string) reflect.Value {
	switch v.Kind() {
	case reflect.String:
		return reflect.ValueOf(r(v.String()))
	case reflect.Slice:
		// e.g. map[string][]string — resolve each element and return a new slice.
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			elem := v.Index(i)
			if elem.Kind() == reflect.String {
				out.Index(i).SetString(r(elem.String()))
			} else {
				out.Index(i).Set(elem)
			}
		}
		return out
	case reflect.Interface:
		if !v.IsNil() {
			inner := applyVarsMapValue(v.Elem(), r)
			if inner.IsValid() {
				return inner
			}
		}
	}
	return reflect.Value{}
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
