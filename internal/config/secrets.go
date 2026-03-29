package config

import (
	"regexp"
	"strings"

	"github.com/rayben/stay-go/internal/secrets"
	"gopkg.in/yaml.v3"
)

// SecretsMap is the in-memory representation of a config secrets: block.
// Each key maps to a SecretEntry describing whether the value is already
// encrypted on disk or still in plaintext (pending first-run encryption).
//
// Custom YAML unmarshaling detects the !encrypted tag on scalar values so
// the config loader never sees or stores plaintext after the first run.
type SecretsMap map[string]secrets.Entry

// UnmarshalYAML implements yaml.Unmarshaler. It reads a mapping node and
// converts each value node: those tagged !encrypted become Encrypted=true
// entries; everything else is treated as a plaintext entry to be encrypted
// on the next write.
func (m *SecretsMap) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return nil
	}
	*m = make(SecretsMap)
	for i := 0; i+1 < len(value.Content); i += 2 {
		keyNode := value.Content[i]
		valNode := value.Content[i+1]
		(*m)[keyNode.Value] = secrets.Entry{
			Encrypted: valNode.Tag == "!encrypted",
			RawValue:  valNode.Value,
			// SourceFile is set by loadLayer after unmarshaling.
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

// MaskSecrets replaces all ${secrets.key_name} tokens in s with <masked>.
// Use this whenever a string containing potential secret references is shown
// to the user (plan display, debug output, etc.).
func MaskSecrets(s string) string {
	if !strings.Contains(s, "${secrets.") {
		return s
	}
	return secretsRefRe.ReplaceAllString(s, "<masked>")
}
