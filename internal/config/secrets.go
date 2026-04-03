package config

import (
	"regexp"
	"strings"

	"github.com/rayben/stay-go/internal/secrets"
	"gopkg.in/yaml.v3"
)

// SecretsMap is the in-memory representation of a config secrets: block.
// Keys are dot-joined paths, supporting both flat and nested YAML:
//
//	Flat:   ssh_id_ed25519: !encrypted ...         → key "ssh_id_ed25519"
//	Nested: ssh:\n  id_ed25519: !encrypted ...     → key "ssh.id_ed25519"
//
// Both forms may coexist within the same secrets: block.
//
// Custom YAML unmarshaling detects the !encrypted tag on scalar values so
// the config loader never sees or stores plaintext after the first run.
type SecretsMap map[string]secrets.Entry

// UnmarshalYAML implements yaml.Unmarshaler. Scalar/encrypted leaves are
// stored directly; plain mapping nodes are recursed with a dot-joined prefix.
func (m *SecretsMap) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return nil
	}
	*m = make(SecretsMap)
	return unmarshalSecretsGroup(value, "", *m)
}

// unmarshalSecretsGroup recursively walks a secrets mapping node, flattening
// nested groups into dot-joined keys in out. prefix is empty at the top level.
func unmarshalSecretsGroup(node *yaml.Node, prefix string, out SecretsMap) error {
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]

		key := keyNode.Value
		if prefix != "" {
			key = prefix + "." + keyNode.Value
		}

		// A plain mapping (no !encrypted tag) is a nested group — recurse.
		if valNode.Kind == yaml.MappingNode && valNode.Tag != "!encrypted" {
			if err := unmarshalSecretsGroup(valNode, key, out); err != nil {
				return err
			}
			continue
		}

		out[key] = secrets.Entry{
			Encrypted: valNode.Tag == "!encrypted",
			RawValue:  valNode.Value,
			// SourceFile and Level are set by loadLayer after unmarshaling.
		}
	}
	return nil
}

// secretsRefRe matches ${secrets.key_name} tokens in command strings.
var secretsRefRe = regexp.MustCompile(`\$\{secrets\.[^}]+\}`)

// ApplySecrets substitutes ${secrets.key_name} tokens in s with their
// decrypted plaintext values, shell-quoted for safe inclusion in bash commands.
// Unreferenced or unknown keys are left as-is.
// Call this at execute time, never at plan/display time.
func ApplySecrets(s string, decrypted map[string]string) string {
	if len(decrypted) == 0 || !strings.Contains(s, "${secrets.") {
		return s
	}
	return secretsRefRe.ReplaceAllStringFunc(s, func(tok string) string {
		key := tok[len("${secrets.") : len(tok)-1]
		if v, ok := decrypted[key]; ok {
			return shellQuote(v)
		}
		return tok
	})
}

// shellQuote wraps s in single quotes and escapes any embedded single quotes,
// producing a string that bash treats as a single literal argument regardless
// of whitespace, newlines, or special characters in s.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ApplySecretsRaw substitutes ${secrets.key_name} tokens in s with their
// decrypted plaintext values without shell-quoting. Use for file content
// (not shell commands). Unknown keys are left as-is.
func ApplySecretsRaw(s string, decrypted map[string]string) string {
	if len(decrypted) == 0 || !strings.Contains(s, "${secrets.") {
		return s
	}
	return secretsRefRe.ReplaceAllStringFunc(s, func(tok string) string {
		key := tok[len("${secrets.") : len(tok)-1]
		if v, ok := decrypted[key]; ok {
			return v
		}
		return tok
	})
}

// ApplySecretsCiphertext substitutes ${secrets.key_name} tokens in s with
// their raw ciphertext values (for hashing only). Unknown keys are left as-is.
func ApplySecretsCiphertext(s string, sm SecretsMap) string {
	if len(sm) == 0 || !strings.Contains(s, "${secrets.") {
		return s
	}
	return secretsRefRe.ReplaceAllStringFunc(s, func(tok string) string {
		key := tok[len("${secrets.") : len(tok)-1]
		if se, ok := sm[key]; ok {
			return se.RawValue
		}
		return tok
	})
}

// MaskSecrets replaces all ${secrets.key_name} tokens in s with <masked>.
// Use this whenever a string containing potential secret references is shown
// to the user (plan display, debug output, etc.).
func MaskSecrets(s string) string {
	if !strings.Contains(s, "${secrets.") {
		return s
	}
	return secretsRefRe.ReplaceAllString(s, "<masked>")
}

// SecretRefs returns the distinct secret key names referenced in s as
// ${secrets.key_name} tokens. Returns nil if there are none.
func SecretRefs(s string) []string {
	if !strings.Contains(s, "${secrets.") {
		return nil
	}
	seen := make(map[string]bool)
	var keys []string
	for _, tok := range secretsRefRe.FindAllString(s, -1) {
		key := tok[len("${secrets.") : len(tok)-1]
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
}
