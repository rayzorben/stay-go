package config

import (
	"testing"

	"github.com/rayzorben/stay-go/internal/secrets"
)

func newResolveCfg() *Config {
	return &Config{
		Vars: map[string]string{"data": "/srv"},
		Secrets: SecretsMap{
			"app.pw":  secrets.Entry{Encrypted: true, RawValue: "CT-PW"},
			"app.key": secrets.Entry{Encrypted: false, RawValue: "pk-key"}, // not yet encrypted
		},
		Files: []FileEntry{
			{Target: "/etc/app.conf", Content: "pw=${secrets.app.pw}\npath=${data}/x\n"},
			{Target: "/etc/k", Source: "${secrets.app.key}"}, // secrets:"-" — stays a token
		},
		Commands: CommandList{
			{Name: "c1", Command: "tool --key=${secrets.app.key} --d=${data}"},
		},
	}
}

func TestApplyVarsLeavesSecretTokens(t *testing.T) {
	cfg := newResolveCfg()
	ApplyVars(cfg, cfg.Vars)

	if got := cfg.Files[0].Content; got != "pw=${secrets.app.pw}\npath=/srv/x\n" {
		t.Errorf("Files[0].Content = %q", got)
	}
	if got := cfg.Files[1].Source; got != "${secrets.app.key}" {
		t.Errorf("Files[1].Source = %q", got)
	}
	if got := cfg.Commands[0].Command; got != "tool --key=${secrets.app.key} --d=/srv" {
		t.Errorf("Commands[0].Command = %q", got)
	}
}

func TestResolveSecretsToCiphertext(t *testing.T) {
	cfg := newResolveCfg()
	ApplyVars(cfg, cfg.Vars)
	ResolveSecretsToCiphertext(cfg)

	if got := cfg.Files[0].Content; got != "pw=CT-PW\npath=/srv/x\n" {
		t.Errorf("Files[0].Content = %q, want ciphertext-substituted", got)
	}
	// secrets:"-" field must keep the token (it is a structural marker).
	if got := cfg.Files[1].Source; got != "${secrets.app.key}" {
		t.Errorf("Files[1].Source = %q, want token preserved", got)
	}
	// Not-yet-encrypted secret: its RawValue (plaintext) is substituted.
	if got := cfg.Commands[0].Command; got != "tool --key=pk-key --d=/srv" {
		t.Errorf("Commands[0].Command = %q", got)
	}
	// The Secrets table itself must be intact.
	if cfg.Secrets["app.pw"].RawValue != "CT-PW" {
		t.Errorf("cfg.Secrets mutated: %+v", cfg.Secrets["app.pw"])
	}
}

func TestSubstituteSecret(t *testing.T) {
	cfg := newResolveCfg()
	ApplyVars(cfg, cfg.Vars)
	ResolveSecretsToCiphertext(cfg)

	SubstituteSecret(cfg, "CT-PW", "actual-password")
	if got := cfg.Files[0].Content; got != "pw=actual-password\npath=/srv/x\n" {
		t.Errorf("after SubstituteSecret: Files[0].Content = %q", got)
	}
	// No-op when ciphertext == plaintext (not-yet-encrypted entries).
	before := cfg.Commands[0].Command
	SubstituteSecret(cfg, "pk-key", "pk-key")
	if cfg.Commands[0].Command != before {
		t.Errorf("no-op SubstituteSecret changed value: %q", cfg.Commands[0].Command)
	}
	// Secrets table still intact.
	if cfg.Secrets["app.pw"].RawValue != "CT-PW" {
		t.Errorf("cfg.Secrets mutated by SubstituteSecret")
	}
}

func TestRenderExternal(t *testing.T) {
	cfg := newResolveCfg()
	ApplyVars(cfg, cfg.Vars)
	ResolveSecretsToCiphertext(cfg)
	cfg.DecryptedSecrets = map[string]string{"app.pw": "actual-password", "app.key": "pk-key"}

	// Tokens, vars, and bare ciphertext (as recovered from state) all resolve.
	got := RenderExternal("DB=${secrets.app.pw} K=${secrets.app.key} D=${data} raw=CT-PW ver=${V:-x}", cfg)
	want := "DB=actual-password K=pk-key D=/srv raw=actual-password ver=${V:-x}"
	if got != want {
		t.Errorf("RenderExternal = %q, want %q", got, want)
	}

	// For-hash variant uses ciphertext, never plaintext.
	gotH := RenderExternalForHash("pw=${secrets.app.pw} d=${data}", cfg)
	if gotH != "pw=CT-PW d=/srv" {
		t.Errorf("RenderExternalForHash = %q", gotH)
	}
}
