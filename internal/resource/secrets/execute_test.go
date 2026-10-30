package secrets

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	secretspkg "github.com/rayzorben/stay-go/internal/secrets"
	"github.com/rayzorben/stay-go/internal/state"
)

// Every non-mutating action must substitute the plaintext into the config;
// a missed action leaves ciphertext in files written later in the same run.
func TestExecuteSubstitutesPlaintext(t *testing.T) {
	for _, action := range []engine.ActionType{
		engine.ActionTrack, engine.ActionAdopt, engine.ActionLevel, engine.ActionUpdate,
	} {
		t.Run(string(action), func(t *testing.T) {
			mgr := secretspkg.NewUnlocked("testpassword")
			ct, err := mgr.Encrypt("hunter2")
			if err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{
				Files: []config.FileEntry{{Target: "/x", Content: `password = "` + ct + `"`}},
			}
			r := &Resource{cfg: cfg, mgr: mgr, entries: map[string]secretspkg.Entry{
				"secrets/pw": {Encrypted: true, RawValue: ct},
			}}
			st, err := state.Load(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}

			node := &engine.PlanNode{ID: "secrets/pw", ResourceType: "secrets", DisplayName: "pw", Action: action}
			if err := r.Execute(context.Background(), node, st); err != nil {
				t.Fatal(err)
			}
			if got, want := cfg.Files[0].Content, `password = "hunter2"`; got != want {
				t.Errorf("content = %q, want %q", got, want)
			}
		})
	}
}
