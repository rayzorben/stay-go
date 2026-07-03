package secrets

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Entry is the in-memory representation of a single secret as loaded from a
// config file. SourceFile tracks which file to update if the value is plain.
type Entry struct {
	Encrypted  bool   // true when the value was stored as !encrypted
	RawValue   string // base64 ciphertext (Encrypted=true) or plaintext
	SourceFile string // absolute path to the originating YAML file
}

// Manager holds the unlocked encryption password and exposes encrypt/decrypt
// operations to the secrets resource.
//
// All secrets across all config layers share one password. The _verify
// sentinel (an encrypted token stored alongside secrets in the YAML file)
// guarantees consistency: a wrong password fails fast before touching anything.
type Manager struct {
	password    string
	unlocked    bool
	newSetup    bool // true when no _verify existed in any config file at Unlock time
}

// New returns an uninitialised Manager. Call Unlock before Encrypt/Decrypt.
func New() *Manager { return &Manager{} }

// IsUnlocked reports whether the Manager has a cached password.
func (m *Manager) IsUnlocked() bool { return m.unlocked }

// Unlock loads the password from the keyring, or prompts the user.
//
// verifyToken is the raw ciphertext of the _verify sentinel (or empty when no
// secrets have ever been encrypted in this config — the first-time case).
//
// First-time setup (verifyToken == ""): prompts twice and requires the two
// entries to match before accepting the password.
//
// Existing setup (verifyToken != ""): prompts once and verifies the password
// decrypts the sentinel correctly (up to 3 attempts).
func (m *Manager) Unlock(verifyToken string) error {
	isNewSetup := verifyToken == ""
	m.newSetup = isNewSetup

	if !isNewSetup {
		// Try the keyring first for existing setups only.
		if pass, ok := loadFromKeyring(); ok {
			if m.checkPassword(pass, verifyToken) == nil {
				m.password = pass
				m.unlocked = true
				return nil
			}
			// Stored password no longer valid — remove it.
			deleteFromKeyring()
		}
	}

	if isNewSetup {
		return m.promptNewPassword()
	}
	return m.promptExistingPassword(verifyToken)
}

// promptNewPassword handles first-time setup: prompts twice, requires match.
func (m *Manager) promptNewPassword() error {
	for attempt := 1; attempt <= 3; attempt++ {
		pass1, err := promptPassword("New secrets password: ")
		if err != nil {
			return err
		}
		pass2, err := promptPassword("Confirm secrets password: ")
		if err != nil {
			return err
		}
		if pass1 != pass2 {
			if attempt < 3 {
				fmt.Fprintln(os.Stderr, "stay-go: passwords do not match, try again")
				continue
			}
			return fmt.Errorf("passwords do not match")
		}
		m.password = pass1
		m.unlocked = true
		storeInKeyring(pass1)
		return nil
	}
	return fmt.Errorf("too many failed attempts")
}

// promptExistingPassword handles an existing encryption setup: prompts once,
// verifies against the sentinel, retries up to 3 times.
func (m *Manager) promptExistingPassword(verifyToken string) error {
	for attempt := 1; attempt <= 3; attempt++ {
		pass, err := promptPassword("Secrets password: ")
		if err != nil {
			return err
		}
		if err := m.checkPassword(pass, verifyToken); err != nil {
			if attempt < 3 {
				fmt.Fprintln(os.Stderr, "stay-go: incorrect password, try again")
				continue
			}
			return fmt.Errorf("incorrect password")
		}
		m.password = pass
		m.unlocked = true
		storeInKeyring(pass)
		return nil
	}
	return fmt.Errorf("too many incorrect password attempts")
}

// checkPassword verifies that password correctly decrypts the sentinel token.
func (m *Manager) checkPassword(password, token string) error {
	pt, err := decrypt(token, password)
	if err != nil || pt != verifyMagic {
		return fmt.Errorf("incorrect password")
	}
	return nil
}

// Encrypt encrypts plaintext with the unlocked password.
func (m *Manager) Encrypt(plaintext string) (string, error) {
	return encrypt(plaintext, m.password)
}

// Decrypt decrypts a base64 ciphertext produced by Encrypt.
func (m *Manager) Decrypt(ciphertext string) (string, error) {
	return decrypt(ciphertext, m.password)
}

// EncryptInFile opens path, encrypts the named plaintext keys in the secrets:
// section (preserving all other file content byte-for-byte), adds a _verify
// sentinel if one doesn't exist, and atomically writes the file back.
//
// Returns a map of key → new ciphertext so callers can derive state hashes
// without re-parsing the file.
func (m *Manager) EncryptInFile(path string, keys []string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", path, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parsing %q: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, nil
	}
	mapping := doc.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return nil, nil
	}
	secretsNode := findMappingValue(mapping, "secrets")
	if secretsNode == nil || secretsNode.Kind != yaml.MappingNode {
		return nil, nil
	}

	toEncrypt := make(map[string]bool, len(keys))
	for _, k := range keys {
		toEncrypt[k] = true
	}

	// Split into lines. yaml.Node.Line is 1-indexed; slice is 0-indexed.
	lines := strings.Split(string(raw), "\n")

	var replacements []encReplacement
	ciphertexts := make(map[string]string, len(keys))

	if err := m.collectEncryptReplacements(secretsNode, "", toEncrypt, lines, ciphertexts, &replacements); err != nil {
		return nil, err
	}

	// Apply replacements bottom-to-top so line indices stay valid.
	sort.Slice(replacements, func(i, j int) bool {
		return replacements[i].startLine > replacements[j].startLine
	})
	for _, r := range replacements {
		newLines := make([]string, 0, len(lines)-(r.endLine-r.startLine))
		newLines = append(newLines, lines[:r.startLine]...)
		newLines = append(newLines, r.newLine)
		newLines = append(newLines, lines[r.endLine+1:]...)
		lines = newLines
	}

	// Append _verify sentinel only if this is a new setup (no _verify existed
	// in any config file when the manager was unlocked) and this file doesn't
	// already have one. This prevents duplicate _verify entries across included
	// config files — the sentinel lives in whichever file first triggered setup.
	if m.newSetup && findMappingValue(secretsNode, VerifyKey) == nil {
		token, err := encrypt(verifyMagic, m.password)
		if err != nil {
			return nil, fmt.Errorf("creating verify token: %w", err)
		}
		indent := "  "
		if len(secretsNode.Content) > 0 {
			indent = strings.Repeat(" ", secretsNode.Content[0].Column-1)
		}
		verifyLine := indent + VerifyKey + ": !encrypted " + token
		insertAt := findSecretsEndInLines(lines, secretsNode.Line-1, indent)
		lines = append(lines[:insertAt], append([]string{verifyLine}, lines[insertAt:]...)...)
	}

	if err := atomicWrite(path, []byte(strings.Join(lines, "\n"))); err != nil {
		return nil, err
	}
	return ciphertexts, nil
}

type encReplacement struct {
	startLine int
	endLine   int
	newLine   string
}

// collectEncryptReplacements recursively walks a secrets mapping node,
// finding leaf scalars whose dot-joined path is in toEncrypt, and appending
// line replacements. prefix is empty at the top level.
func (m *Manager) collectEncryptReplacements(node *yaml.Node, prefix string, toEncrypt map[string]bool, lines []string, ciphertexts map[string]string, replacements *[]encReplacement) error {
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]

		dotKey := keyNode.Value
		if prefix != "" {
			dotKey = prefix + "." + keyNode.Value
		}

		// Nested group — recurse.
		if valNode.Kind == yaml.MappingNode && valNode.Tag != "!encrypted" {
			if err := m.collectEncryptReplacements(valNode, dotKey, toEncrypt, lines, ciphertexts, replacements); err != nil {
				return err
			}
			continue
		}

		if !toEncrypt[dotKey] {
			continue
		}

		isBlock := valNode.Style == yaml.LiteralStyle || valNode.Style == yaml.FoldedStyle
		if !isBlock && strings.Contains(valNode.Value, "\n") {
			return fmt.Errorf("secret %q: multi-line quoted strings are not supported; use a block scalar (|)", dotKey)
		}

		ct, err := encrypt(valNode.Value, m.password)
		if err != nil {
			return fmt.Errorf("encrypting secret %q: %w", dotKey, err)
		}
		ciphertexts[dotKey] = ct

		keyLineIdx := keyNode.Line - 1
		colIdx := valNode.Column - 1

		if isBlock {
			keyIndent := countIndent(lines[keyLineIdx])
			endLineIdx := findBlockEndLine(lines, keyLineIdx, keyIndent)
			newLine := lines[keyLineIdx][:colIdx] + "!encrypted " + ct
			*replacements = append(*replacements, encReplacement{keyLineIdx, endLineIdx, newLine})
		} else {
			lineIdx := valNode.Line - 1
			if lineIdx < 0 || lineIdx >= len(lines) || colIdx < 0 || colIdx > len(lines[lineIdx]) {
				return fmt.Errorf("secret %q: unexpected position line=%d col=%d", dotKey, valNode.Line, valNode.Column)
			}
			newLine := lines[lineIdx][:colIdx] + "!encrypted " + ct
			*replacements = append(*replacements, encReplacement{lineIdx, lineIdx, newLine})
		}
	}
	return nil
}

// findMappingValue returns the value node for key in a yaml MappingNode, or nil.
func findMappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// countIndent returns the number of leading spaces on a line.
func countIndent(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

// findBlockEndLine returns the 0-indexed last line of a block scalar that
// starts on keyLineIdx. Block content lines are indented more than keyIndent.
func findBlockEndLine(lines []string, keyLineIdx, keyIndent int) int {
	last := keyLineIdx
	for i := keyLineIdx + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue // blank lines may appear inside a block
		}
		if countIndent(line) > keyIndent {
			last = i
		} else {
			break
		}
	}
	return last
}

// findSecretsEndInLines returns the 0-indexed line at which to insert the
// _verify sentinel. It scans forward from secretsNodeLineIdx looking for the
// first non-blank line whose indentation is ≤ the secrets block's own indent
// (i.e. a sibling or parent key), and inserts just before it.
func findSecretsEndInLines(lines []string, secretsNodeLineIdx int, blockIndent string) int {
	indent := len(blockIndent)
	for i := secretsNodeLineIdx + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if countIndent(line) <= indent {
			return i
		}
	}
	return len(lines)
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
