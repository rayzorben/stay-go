package secrets

import (
	"context"
	"fmt"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	secretspkg "github.com/rayben/stay-go/internal/secrets"
	"github.com/rayben/stay-go/internal/state"
)

// Execute carries out a single secrets plan node.
//
//   - ADD: unlock the manager (prompting if needed), encrypt the plaintext
//     value in-place in its source file, populate cfg.DecryptedSecrets.
//   - TRACK/UPDATE: unlock the manager, decrypt the ciphertext, populate
//     cfg.DecryptedSecrets.
//   - REMOVE: clear the state entry only; no file modification.
//
// The Manager is unlocked on the first call that needs it; subsequent calls
// reuse the cached password.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	switch node.Action {
	case engine.ActionAdd:
		entry := r.entries[node.ID]

		if err := r.ensureUnlocked(); err != nil {
			return err
		}

		// Encrypt the plaintext value in-place in its source file.
		key := node.DisplayName
		ciphertexts, err := r.mgr.EncryptInFile(entry.SourceFile, []string{key})
		if err != nil {
			return fmt.Errorf("encrypting secret %q: %w", key, err)
		}

		// The plaintext is immediately available; no need to decrypt.
		if r.cfg.DecryptedSecrets == nil {
			r.cfg.DecryptedSecrets = make(map[string]string)
		}
		r.cfg.DecryptedSecrets[key] = entry.RawValue

		// State hash is based on the new ciphertext (stable until re-encrypted).
		newCT := ciphertexts[key]
		stateHash := config.Hash(newCT)
		st.Set(node.ID, stateHash, node.Level, nil)

	case engine.ActionTrack, engine.ActionAdopt, engine.ActionUpdate:
		entry := r.entries[node.ID]
		if !entry.Encrypted {
			break
		}

		if err := r.ensureUnlocked(); err != nil {
			return err
		}

		pt, err := r.mgr.Decrypt(entry.RawValue)
		if err != nil {
			return fmt.Errorf("decrypting secret %q: %w", node.DisplayName, err)
		}
		if r.cfg.DecryptedSecrets == nil {
			r.cfg.DecryptedSecrets = make(map[string]string)
		}
		r.cfg.DecryptedSecrets[node.DisplayName] = pt
		if node.Action != engine.ActionTrack {
			st.Set(node.ID, node.ConfigHash, node.Level, nil)
		}

	case engine.ActionRemove:
		// No file modification — removing a secret from config simply stops
		// tracking it. The !encrypted line remains in the YAML file (the user
		// chose to remove it from management, not delete the value).
		st.Delete(node.ID)
	}

	return nil
}

// ensureUnlocked unlocks the Manager on the first call that needs it.
// It finds the _verify sentinel from cfg.Secrets to determine whether this
// is a new setup (double-prompt) or an existing one (single-prompt + verify).
func (r *Resource) ensureUnlocked() error {
	if r.mgr.IsUnlocked() {
		return nil
	}

	var verifyToken string
	if ve, ok := r.cfg.Secrets[secretspkg.VerifyKey]; ok && ve.Encrypted {
		verifyToken = ve.RawValue
	}

	return r.mgr.Unlock(verifyToken)
}
