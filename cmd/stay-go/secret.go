package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/rayzorben/stay-go/internal/config"
	secretspkg "github.com/rayzorben/stay-go/internal/secrets"
)

const secretUsage = `Usage:
  stay-go [-c dir] secret update <key> [value] [--all]

Replaces an existing secret's value and encrypts it in place in the config.
<key> is the dot-joined name used in ${secrets.<key>} (e.g. kopia.repository_password).

  value   the new plaintext. Omit it to be prompted (hidden, no shell history),
          or pipe it on stdin: stay-go secret update ssh.id_rsa < ~/.ssh/id_rsa
          (piped input keeps its content; a single line loses its trailing newline).
          Piping needs the secrets password cached in the keyring, since the
          password prompt reads the same stdin; any earlier run caches it.
  --all   update every config file that defines <key>, not just the one this
          host resolves it from.

The next apply sees the new ciphertext and re-runs whatever uses the secret.
`

// unlockSecrets returns a Manager unlocked from the keyring or a password
// prompt. A variable so tests can supply a known password.
var unlockSecrets = func(verifyToken string) (*secretspkg.Manager, error) {
	mgr := secretspkg.New()
	return mgr, mgr.Unlock(verifyToken)
}

// runSecret implements `stay-go secret …`. It edits config files only — no
// plan, no state, no run log.
func runSecret(configDir string, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(os.Stderr, secretUsage)
		return nil
	}
	if args[0] != "update" {
		return fmt.Errorf("secret: unknown subcommand %q\n\n%s", args[0], secretUsage)
	}

	var all bool
	var positional []string
	for _, a := range args[1:] {
		switch a {
		case "--all", "-all":
			all = true
		case "-h", "--help":
			fmt.Fprint(os.Stderr, secretUsage)
			return nil
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) < 1 || len(positional) > 2 {
		return fmt.Errorf("secret update: expected <key> [value]\n\n%s", secretUsage)
	}
	key := positional[0]
	if key == secretspkg.VerifyKey {
		return fmt.Errorf("secret update: %s is the password sentinel, not a secret", key)
	}

	cfg, err := config.LoadAll(configDir)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	verify, ok := cfg.Secrets[secretspkg.VerifyKey]
	if !ok || !verify.Encrypted {
		return errors.New("secret update: no secrets password is set up yet; add the secret as plaintext in the YAML and run stay-go once")
	}

	definedIn, err := filesDefiningSecret(configDir, key)
	if err != nil {
		return err
	}
	var targets []string
	if all {
		targets = definedIn
	} else if entry, ok := cfg.Secrets[key]; ok {
		targets = []string{entry.SourceFile}
	}
	if len(targets) == 0 {
		if len(definedIn) > 0 {
			return fmt.Errorf("secret %q is not used on this host (defined in %s); pass --all to update it there",
				key, strings.Join(relPaths(configDir, definedIn), ", "))
		}
		return fmt.Errorf("secret %q is not defined in any config file; add it as plaintext in a secrets: block and run stay-go once", key)
	}

	mgr, err := unlockSecrets(verify.RawValue)
	if err != nil {
		return fmt.Errorf("unlocking secrets: %w", err)
	}

	var value string
	if len(positional) == 2 {
		value = positional[1]
	} else if value, err = readSecretValue(key); err != nil {
		return err
	}
	if value == "" {
		return errors.New("secret update: value cannot be empty")
	}

	for _, path := range targets {
		if _, err := mgr.SetInFile(path, key, value); err != nil {
			return fmt.Errorf("updating %s: %w", path, err)
		}
		fmt.Printf("updated %s in %s\n", key, relPath(configDir, path))
	}
	if !all {
		var others []string
		for _, p := range definedIn {
			if p != targets[0] {
				others = append(others, p)
			}
		}
		if len(others) > 0 {
			fmt.Printf("also defined in %s (unchanged; use --all to update those too)\n",
				strings.Join(relPaths(configDir, others), ", "))
		}
	}
	return nil
}

// readSecretValue prompts twice without echo when stdin is a terminal, else
// reads all of stdin.
func readSecretValue(key string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading value from stdin: %w", err)
		}
		v := string(raw)
		// `echo value |` appends a newline nobody means as part of a token;
		// multi-line content (keys, certificates) is kept exactly.
		if trimmed := strings.TrimSuffix(v, "\n"); !strings.Contains(trimmed, "\n") {
			v = strings.TrimSuffix(trimmed, "\r")
		}
		return v, nil
	}
	read := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(raw), err
	}
	v1, err := read(fmt.Sprintf("New value for %s: ", key))
	if err != nil {
		return "", fmt.Errorf("reading value: %w", err)
	}
	v2, err := read("Confirm: ")
	if err != nil {
		return "", fmt.Errorf("reading value: %w", err)
	}
	if v1 != v2 {
		return "", errors.New("values do not match")
	}
	return v1, nil
}

// filesDefiningSecret returns every .yaml/.yml file under configDir (absolute
// paths, sorted by walk order) whose secrets: section defines key.
func filesDefiningSecret(configDir, key string) ([]string, error) {
	root, err := filepath.Abs(configDir)
	if err != nil {
		return nil, err
	}
	var out []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml") {
			return nil
		}
		ok, err := secretspkg.FileDefinesSecret(path, key)
		if err != nil {
			return err
		}
		if ok {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", configDir, err)
	}
	return out, nil
}

func relPath(configDir, path string) string {
	root, err := filepath.Abs(configDir)
	if err != nil {
		return path
	}
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join(configDir, rel)
	}
	return path
}

func relPaths(configDir string, paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = relPath(configDir, p)
	}
	return out
}
