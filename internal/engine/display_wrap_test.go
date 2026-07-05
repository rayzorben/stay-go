package engine

import (
	"strings"
	"testing"
)

// Error notes must WRAP, never truncate: the tail of stderr often holds the
// remediation command (e.g. a full "tailscale up --auth-key=..." line).
func TestWrapRunes_preservesAllContent(t *testing.T) {
	long := "tailscale up --auth-key=tskey-auth-XXXXXXXXXXXXXXXX --operator=rayben --accept-routes --exit-node= --advertise-tags="
	segs := wrapRunes(long, 40)
	if len(segs) < 3 {
		t.Fatalf("expected multiple wrapped segments, got %d", len(segs))
	}
	joined := strings.Join(segs, " ")
	// Rejoining on single spaces must reproduce every original token.
	for _, tok := range strings.Fields(long) {
		if !strings.Contains(joined, tok) {
			t.Fatalf("token %q lost in wrap output %q", tok, joined)
		}
	}
	for i, s := range segs {
		if len([]rune(s)) > 40 {
			t.Fatalf("segment %d exceeds width: %q", i, s)
		}
	}
}

func TestWrapRunes_hardSplitsLongTokens(t *testing.T) {
	tok := strings.Repeat("x", 95)
	segs := wrapRunes(tok, 40)
	if got := strings.Join(segs, ""); got != tok {
		t.Fatalf("long token mangled: %q", got)
	}
}

func TestWrapRunes_emptyLinePreserved(t *testing.T) {
	if segs := wrapRunes("", 40); len(segs) != 1 || segs[0] != "" {
		t.Fatalf("empty line must yield one empty segment, got %#v", segs)
	}
}
