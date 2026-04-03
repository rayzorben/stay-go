package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTemp writes content to a temp file and returns its path.
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stay-go-test-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

const sampleYAML = `variables:
  foo: bar

packages:
  - neovim
  - git

# this comment should survive
secrets:
  api_key: plaintext-value
  db_pass: another-plain-value

commands:
  - name: Do Something
    command: echo hello
`

func TestEncryptInFilePreservesFormatting(t *testing.T) {
	path := writeTemp(t, sampleYAML)

	mgr := &Manager{password: "testpassword", unlocked: true, newSetup: true}
	if _, err := mgr.EncryptInFile(path, []string{"api_key", "db_pass"}); err != nil {
		t.Fatalf("encryptInFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result := string(got)

	// Structure outside secrets must be byte-identical.
	wantPrefix := "variables:\n  foo: bar\n\npackages:\n  - neovim\n  - git\n\n# this comment should survive\nsecrets:\n"
	if !strings.HasPrefix(result, wantPrefix) {
		t.Errorf("prefix changed:\ngot:  %q\nwant: %q", result[:min(len(result), len(wantPrefix)+20)], wantPrefix)
	}

	// Commands section must be unchanged.
	if !strings.Contains(result, "commands:\n  - name: Do Something\n    command: echo hello\n") {
		t.Error("commands section was altered")
	}

	// Secrets values must now carry the !encrypted tag.
	if !strings.Contains(result, "api_key: !encrypted ") {
		t.Error("api_key not encrypted")
	}
	if !strings.Contains(result, "db_pass: !encrypted ") {
		t.Error("db_pass not encrypted")
	}

	// _verify sentinel must have been appended inside the secrets block.
	if !strings.Contains(result, "_verify: !encrypted ") {
		t.Error("_verify sentinel missing")
	}

	// Verify _verify appears before "commands:" (i.e. still inside secrets block).
	verifyPos := strings.Index(result, "_verify:")
	commandsPos := strings.Index(result, "commands:")
	if verifyPos == -1 || commandsPos == -1 || verifyPos > commandsPos {
		t.Errorf("_verify not positioned inside secrets block (verify=%d commands=%d)", verifyPos, commandsPos)
	}
}

func TestEncryptInFileDecryptable(t *testing.T) {
	path := writeTemp(t, sampleYAML)

	mgr := &Manager{password: "roundtrip-pw", unlocked: true}
	if _, err := mgr.EncryptInFile(path, []string{"api_key", "db_pass"}); err != nil {
		t.Fatalf("encryptInFile: %v", err)
	}

	// Re-parse and verify the encrypted values decrypt correctly.
	entry := Entry{}
	raw, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "api_key: !encrypted ") {
			entry.RawValue = strings.TrimPrefix(line, "api_key: !encrypted ")
			entry.Encrypted = true
		}
	}
	if !entry.Encrypted {
		t.Fatal("api_key not found as encrypted in output")
	}

	pt, err := decrypt(entry.RawValue, "roundtrip-pw")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if pt != "plaintext-value" {
		t.Errorf("got %q, want %q", pt, "plaintext-value")
	}
}

func TestEncryptInFileIdempotent(t *testing.T) {
	// Running encryptInFile twice on a file that already has all values
	// encrypted should not change the file (no new _verify is added since
	// it already exists after the first run).
	path := writeTemp(t, sampleYAML)
	mgr := &Manager{password: "pw", unlocked: true}

	if _, err := mgr.EncryptInFile(path, []string{"api_key", "db_pass"}); err != nil {
		t.Fatal(err)
	}
	after1, _ := os.ReadFile(path)

	// Second call with no plaintext keys to encrypt.
	if _, err := mgr.EncryptInFile(path, nil); err != nil {
		t.Fatal(err)
	}
	after2, _ := os.ReadFile(path)

	if string(after1) != string(after2) {
		t.Error("second call modified the file")
	}
}

func TestEncryptInFileOnlySecretsSectionModified(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	mgr := &Manager{password: "pw", unlocked: true}

	if _, err := mgr.EncryptInFile(path, []string{"api_key", "db_pass"}); err != nil {
		t.Fatal(err)
	}

	result, _ := os.ReadFile(path)

	// Count unchanged lines to make sure non-secrets lines are identical.
	unchanged := []string{
		"variables:",
		"  foo: bar",
		"",
		"packages:",
		"  - neovim",
		"  - git",
		"",
		"# this comment should survive",
		"secrets:",
	}
	for _, want := range unchanged {
		if !strings.Contains(string(result), want+"\n") {
			t.Errorf("line %q missing or altered", want)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestEncryptInFileMissingFile(t *testing.T) {
	mgr := &Manager{password: "pw"}
	_, err := mgr.EncryptInFile(filepath.Join(t.TempDir(), "nonexistent.yaml"), []string{"key"})
	if err == nil {
		t.Error("expected error for missing file")
	}
}
