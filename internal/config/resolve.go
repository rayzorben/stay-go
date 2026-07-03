// Package config — universal substitution pipeline.
//
// Resolution is intentionally centralised here so that resources never deal
// with ${var}, ${env:VAR}, $(cmd), ~, or ${secrets.x} themselves: by the time
// a resource sees the Config, every string field is fully resolved.
//
// Pipeline (see LoadAll and the secrets resource):
//
//  1. LoadAll       — resolve ${var}/${env:VAR}/$(cmd)/~ in every string field
//                     (ApplyVars), then replace every ${secrets.x} token with the
//                     secret's *ciphertext* (ResolveSecretsToCiphertext). After
//                     this the Config holds no ${secrets.x} tokens and no
//                     plaintext — safe to display and to hash.
//  2. execute phase — the secrets resource decrypts each secret and calls
//                     SubstituteSecret, replacing its ciphertext with the
//                     plaintext throughout the Config. The engine orders all
//                     secrets nodes before every other node, so by the time any
//                     other resource executes the Config is fully resolved.
//
// Content loaded from *outside* the Config tree (a file a resource reads at
// execute time) cannot be pre-resolved; such resources call RenderExternal /
// RenderExternalForHash — the only substitution API resources ever touch.
//
// A struct field tagged `secrets:"-"` is excluded from secret substitution
// (but still gets ${var} resolution). Used where a ${secrets.x} token is a
// structural marker rather than an inline value (e.g. FileEntry.Source), or
// where a value is serialised verbatim for a separate stay-go invocation.
package config

import (
	"reflect"
	"strings"
)

// ResolveSecretsToCiphertext replaces every ${secrets.x} token in cfg's string
// fields with the secret's ciphertext (or, for entries not yet encrypted, its
// plaintext value, which is all that exists). Run once at load time. Fields
// tagged secrets:"-" — and the Secrets map itself — are left untouched.
func ResolveSecretsToCiphertext(cfg *Config) {
	if len(cfg.Secrets) == 0 {
		return
	}
	sm := cfg.Secrets
	cfg.Secrets = nil // never rewrite the secrets table itself
	walkConfigStrings(cfg, skipSecretAware, func(s string) string { return ApplySecretsCiphertext(s, sm) })
	cfg.Secrets = sm
}

// SubstituteSecret replaces every occurrence of ciphertext with plaintext in
// cfg's string fields. The secrets resource calls this for each secret as it is
// decrypted during execution; ordering (secrets first) is enforced by the
// engine, so all later resources observe the resolved plaintext. No-op when the
// value was never encrypted (ciphertext == plaintext). Fields tagged
// secrets:"-" — and the Secrets map itself — are left untouched.
func SubstituteSecret(cfg *Config, ciphertext, plaintext string) {
	if ciphertext == "" || ciphertext == plaintext {
		return
	}
	sm := cfg.Secrets
	cfg.Secrets = nil
	walkConfigStrings(cfg, skipSecretAware, func(s string) string {
		if strings.Contains(s, ciphertext) {
			return strings.ReplaceAll(s, ciphertext, plaintext)
		}
		return s
	})
	cfg.Secrets = sm
}

// RenderExternal resolves ${var}/${env:VAR}/$(cmd)/~ and ${secrets.x} (to the
// decrypted plaintext) in content that was loaded from outside the Config tree
// — e.g. a file a resource reads at execute time, or a value recovered from the
// state file (where secret references were persisted as ciphertext). It also
// replaces any bare secret ciphertext it finds. cfg.DecryptedSecrets must be
// populated, which it is by the time any non-secrets resource executes.
// This is the only substitution helper resources should call.
func RenderExternal(content string, cfg *Config) string {
	s := ResolveString(content, cfg.Vars)
	s = ApplySecretsRaw(s, cfg.DecryptedSecrets)
	return substituteAllCiphertexts(s, cfg)
}

// substituteAllCiphertexts replaces every known encrypted-secret ciphertext
// appearing in s with the decrypted plaintext.
func substituteAllCiphertexts(s string, cfg *Config) string {
	for key, entry := range cfg.Secrets {
		if !entry.Encrypted {
			continue
		}
		if pt, ok := cfg.DecryptedSecrets[key]; ok && strings.Contains(s, entry.RawValue) {
			s = strings.ReplaceAll(s, entry.RawValue, pt)
		}
	}
	return s
}

// RenderExternalForHash is the plan-time counterpart of RenderExternal: it
// resolves ${var}/$(cmd)/~ but substitutes ${secrets.x} with the secret's
// ciphertext (stable across runs, never plaintext) so external content can be
// hashed without unlocking the secrets store.
func RenderExternalForHash(content string, cfg *Config) string {
	return ApplySecretsCiphertext(ResolveString(content, cfg.Vars), cfg.Secrets)
}

// ─── Reflection walk ──────────────────────────────────────────────────────────

// skipInternal excludes fields tagged yaml:"-" (internal-only, must not be
// mutated by substitution).
func skipInternal(f reflect.StructField) bool { return f.Tag.Get("yaml") == "-" }

// skipSecretAware additionally excludes fields tagged secrets:"-".
func skipSecretAware(f reflect.StructField) bool {
	return f.Tag.Get("yaml") == "-" || f.Tag.Get("secrets") == "-"
}

// walkConfigStrings visits every settable string in cfg's value tree (following
// pointers, interfaces, slices, arrays and maps) and replaces it with
// transform(s). Struct fields for which skip returns true are not descended.
func walkConfigStrings(cfg *Config, skip func(reflect.StructField) bool, transform func(string) string) {
	walkValue(reflect.ValueOf(cfg), skip, transform)
}

func walkValue(v reflect.Value, skip func(reflect.StructField) bool, transform func(string) string) {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if !v.IsNil() {
			walkValue(v.Elem(), skip, transform)
		}
	case reflect.Struct:
		t := v.Type()
		for i := range v.NumField() {
			if skip(t.Field(i)) {
				continue
			}
			walkValue(v.Field(i), skip, transform)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			walkValue(v.Index(i), skip, transform)
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			if nv := walkMapValue(v.MapIndex(key), transform); nv.IsValid() {
				v.SetMapIndex(key, nv)
			}
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(transform(v.String()))
		}
	}
}

// walkMapValue resolves strings inside a (non-addressable) map value and returns
// the replacement to store, or an invalid Value when there is nothing to change.
// Handles string values and []string values (the Depends shape); other kinds are
// left alone.
func walkMapValue(v reflect.Value, transform func(string) string) reflect.Value {
	switch v.Kind() {
	case reflect.String:
		return reflect.ValueOf(transform(v.String()))
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return reflect.Value{}
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).SetString(transform(v.Index(i).String()))
		}
		return out
	case reflect.Interface:
		if !v.IsNil() {
			if inner := walkMapValue(v.Elem(), transform); inner.IsValid() {
				return inner
			}
		}
	}
	return reflect.Value{}
}
