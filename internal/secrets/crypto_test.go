package secrets

import (
	"strings"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	cases := []struct {
		name      string
		plaintext string
		password  string
	}{
		{"simple", "hello world", "hunter2"},
		{"empty plaintext", "", "password"},
		{"unicode", "café ☕ secreto", "pässwörd"},
		{"long value", strings.Repeat("a", 4096), "key"},
		{"special chars", "ssh-ed25519 AAAA/+= user@host", "p4$$w0rd!"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ct, err := encrypt(tc.plaintext, tc.password)
			if err != nil {
				t.Fatalf("encrypt: %v", err)
			}
			if ct == tc.plaintext {
				t.Error("ciphertext equals plaintext")
			}

			got, err := decrypt(ct, tc.password)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			if got != tc.plaintext {
				t.Errorf("got %q, want %q", got, tc.plaintext)
			}
		})
	}
}

func TestDecryptWrongPassword(t *testing.T) {
	ct, err := encrypt("secret", "correct-password")
	if err != nil {
		t.Fatal(err)
	}
	_, err = decrypt(ct, "wrong-password")
	if err == nil {
		t.Error("expected error decrypting with wrong password")
	}
}

func TestEncryptProducesUniqueCiphertexts(t *testing.T) {
	// Two encryptions of the same plaintext must differ (different salt+nonce).
	ct1, _ := encrypt("same plaintext", "same password")
	ct2, _ := encrypt("same plaintext", "same password")
	if ct1 == ct2 {
		t.Error("two encryptions of the same value produced identical ciphertexts")
	}
}

func TestDecryptMalformed(t *testing.T) {
	cases := []string{
		"",
		"notbase64!!!",
		"dG9vc2hvcnQ=", // too short when decoded
	}
	for _, c := range cases {
		if _, err := decrypt(c, "password"); err == nil {
			t.Errorf("expected error for malformed input %q", c)
		}
	}
}

func TestVerifyMagic(t *testing.T) {
	// Simulates the _verify sentinel: encrypt verifyMagic, decrypt and check.
	ct, err := encrypt(verifyMagic, "my-password")
	if err != nil {
		t.Fatal(err)
	}
	pt, err := decrypt(ct, "my-password")
	if err != nil {
		t.Fatal(err)
	}
	if pt != verifyMagic {
		t.Errorf("got %q, want %q", pt, verifyMagic)
	}
}
