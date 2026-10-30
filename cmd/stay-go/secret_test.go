package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	secretspkg "github.com/rayzorben/stay-go/internal/secrets"
)

// secretFixture writes a config dir whose default.yaml defines tok (and the
// _verify sentinel) and whose other.yaml — not included — defines tok too.
// Returns the dir and a Manager unlocked with the fixture's password.
func secretFixture(t *testing.T) (string, *secretspkg.Manager) {
	t.Helper()
	mgr := secretspkg.NewUnlocked("pw")
	enc := func(s string) string {
		ct, err := mgr.Encrypt(s)
		if err != nil {
			t.Fatal(err)
		}
		return ct
	}
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("default.yaml", "secrets:\n  _verify: !encrypted "+enc("stay-go-secrets-v1")+"\n  tok: !encrypted "+enc("old")+"\n")
	write("other.yaml", "secrets:\n  tok: !encrypted "+enc("old-other")+"\n")

	prev := unlockSecrets
	unlockSecrets = func(string) (*secretspkg.Manager, error) { return mgr, nil }
	t.Cleanup(func() { unlockSecrets = prev })
	return dir, mgr
}

// secretValue decrypts tok from the given file in dir.
func secretValue(t *testing.T, mgr *secretspkg.Manager, dir, file string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if ct, ok := strings.CutPrefix(strings.TrimSpace(line), "tok: !encrypted "); ok {
			pt, err := mgr.Decrypt(ct)
			if err != nil {
				t.Fatal(err)
			}
			return pt
		}
	}
	t.Fatalf("tok not found in %s:\n%s", file, raw)
	return ""
}

func TestSecretUpdateResolvedFileOnly(t *testing.T) {
	dir, mgr := secretFixture(t)
	if err := runSecret(dir, []string{"update", "tok", "new"}); err != nil {
		t.Fatal(err)
	}
	if got := secretValue(t, mgr, dir, "default.yaml"); got != "new" {
		t.Errorf("default.yaml tok = %q, want new", got)
	}
	if got := secretValue(t, mgr, dir, "other.yaml"); got != "old-other" {
		t.Errorf("other.yaml tok = %q, want it unchanged", got)
	}
}

func TestSecretUpdateAll(t *testing.T) {
	dir, mgr := secretFixture(t)
	if err := runSecret(dir, []string{"update", "tok", "--all", "new"}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"default.yaml", "other.yaml"} {
		if got := secretValue(t, mgr, dir, f); got != "new" {
			t.Errorf("%s tok = %q, want new", f, got)
		}
	}
}

func TestSecretUpdateErrors(t *testing.T) {
	dir, _ := secretFixture(t)
	for _, args := range [][]string{
		{"update", "missing", "v"},
		{"update", "_verify", "v"},
		{"update"},
		{"rotate", "tok"},
	} {
		if err := runSecret(dir, args); err == nil {
			t.Errorf("runSecret(%v): expected an error", args)
		}
	}
}
