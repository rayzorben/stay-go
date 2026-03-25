package secrets

import (
	"fmt"
	"os"
	"syscall"

	gokeyring "github.com/zalando/go-keyring"
	"golang.org/x/term"
)

const (
	keyringService = "stay-go"
	keyringAccount = "secrets-password"
)

// loadFromKeyring attempts to retrieve the stored encryption password.
// Returns ("", false) silently on any failure (no keyring daemon, not found, etc.).
func loadFromKeyring() (string, bool) {
	pass, err := gokeyring.Get(keyringService, keyringAccount)
	if err != nil {
		return "", false
	}
	return pass, true
}

// storeInKeyring saves the password to the system keyring.
// Failure is intentionally silent — the user will just be prompted next time.
func storeInKeyring(password string) {
	_ = gokeyring.Set(keyringService, keyringAccount, password)
}

// deleteFromKeyring removes the stored password. Used when the stored password
// is found to be wrong (e.g. after a password change).
func deleteFromKeyring() {
	_ = gokeyring.Delete(keyringService, keyringAccount)
}

// promptPassword prompts the user on stderr and reads a password without echo.
func promptPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	raw, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr) // restore newline after hidden input
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("password cannot be empty")
	}
	return string(raw), nil
}
