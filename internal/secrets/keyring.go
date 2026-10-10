package secrets

import (
	"fmt"
	"os"
	"syscall"
	"time"

	gokeyring "github.com/zalando/go-keyring"
	"golang.org/x/term"
)

const (
	keyringService = "stay-go"
	keyringAccount = "secrets-password"

	// keyringTimeout bounds every keyring call. A locked Secret Service
	// (e.g. gnome-keyring over SSH) blocks until someone answers an unlock
	// prompt on a display nobody is looking at; treat that as unavailable.
	keyringTimeout = 3 * time.Second
)

// withKeyringTimeout runs fn in a goroutine and gives up after keyringTimeout.
// A timed-out call is abandoned (go-keyring offers no cancellation); the
// goroutine dies with the process.
func withKeyringTimeout(fn func() (string, error)) (string, error) {
	type result struct {
		val string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.val, r.err
	case <-time.After(keyringTimeout):
		return "", fmt.Errorf("keyring did not respond within %s", keyringTimeout)
	}
}

// loadFromKeyring attempts to retrieve the stored encryption password.
// Returns ("", false) silently on any failure (no keyring daemon, not found, etc.).
func loadFromKeyring() (string, bool) {
	pass, err := withKeyringTimeout(func() (string, error) {
		return gokeyring.Get(keyringService, keyringAccount)
	})
	if err != nil {
		return "", false
	}
	return pass, true
}

// storeInKeyring saves the password to the system keyring.
// Failure is intentionally silent — the user will just be prompted next time.
func storeInKeyring(password string) {
	_, _ = withKeyringTimeout(func() (string, error) {
		return "", gokeyring.Set(keyringService, keyringAccount, password)
	})
}

// deleteFromKeyring removes the stored password. Used when the stored password
// is found to be wrong (e.g. after a password change).
func deleteFromKeyring() {
	_, _ = withKeyringTimeout(func() (string, error) {
		return "", gokeyring.Delete(keyringService, keyringAccount)
	})
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
