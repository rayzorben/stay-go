// Package secrets handles encryption and decryption of secret values stored
// in config files, plus keyring-backed password management.
//
// Encryption uses Argon2id (memory-hard KDF) + AES-256-GCM (authenticated
// encryption). Each value gets a unique random salt and nonce, so two
// encryptions of the same plaintext produce different ciphertexts.
//
// On-disk format (base64-encoded):
//
//	[ salt 16 bytes ] [ nonce 12 bytes ] [ GCM ciphertext+tag ]
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	saltLen  = 16
	nonceLen = 12
	keyLen   = 32

	// Argon2id parameters. Tuned for interactive use (~150 ms on modern hw).
	// These are embedded in the binary; changing them requires re-encryption
	// of all existing secrets.
	kdfTime    = 3
	kdfMem     = 64 * 1024 // 64 MiB
	kdfThreads = 4

	// verifyMagic is the plaintext of the _verify sentinel that proves the
	// correct password is being used across all secrets in a file.
	verifyMagic = "stay-go-secrets-v1"

	// VerifyKey is the reserved secrets map key used for the password-
	// verification sentinel. User-defined secrets must not use this name.
	VerifyKey = "_verify"
)

// encrypt encrypts plaintext with password using Argon2id + AES-256-GCM.
// Returns a base64-encoded blob containing salt + nonce + ciphertext+tag.
func encrypt(plaintext, password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}

	gcm, err := newGCM(deriveKey(password, salt))
	if err != nil {
		return "", err
	}

	ct := gcm.Seal(nil, nonce, []byte(plaintext), nil)

	blob := make([]byte, saltLen+nonceLen+len(ct))
	copy(blob[:saltLen], salt)
	copy(blob[saltLen:saltLen+nonceLen], nonce)
	copy(blob[saltLen+nonceLen:], ct)

	return base64.StdEncoding.EncodeToString(blob), nil
}

// decrypt decrypts a base64-encoded blob produced by encrypt.
// Returns a clear "wrong password" error so callers can prompt again.
func decrypt(encoded, password string) (string, error) {
	blob, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("malformed secret (invalid base64): %w", err)
	}
	const minLen = saltLen + nonceLen + 16 // 16 = GCM auth tag
	if len(blob) < minLen {
		return "", errors.New("malformed secret: too short")
	}

	salt := blob[:saltLen]
	nonce := blob[saltLen : saltLen+nonceLen]
	ct := blob[saltLen+nonceLen:]

	gcm, err := newGCM(deriveKey(password, salt))
	if err != nil {
		return "", err
	}

	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		// GCM authentication failure → wrong password or corrupted data.
		return "", errors.New("wrong password or corrupted secret")
	}

	return string(pt), nil
}

func deriveKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, kdfTime, kdfMem, kdfThreads, keyLen)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}
	return gcm, nil
}
