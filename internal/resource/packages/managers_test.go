package packages

import (
	"testing"
)

func TestUsesPacmanQqList(t *testing.T) {
	if !usesPacmanQqList(&PackageManager{ListCmd: []string{"pacman", "-Qq"}}) {
		t.Fatal("expected true for pacman -Qq")
	}
	if usesPacmanQqList(&PackageManager{ListCmd: []string{"pacman", "-Q"}}) {
		t.Fatal("expected false for other pacman list")
	}
	if usesPacmanQqList(nil) {
		t.Fatal("expected false for nil")
	}
}

func TestParseXBPS(t *testing.T) {
	raw := `ii bash-5.2.032_1        The GNU Bourne Again shell
ii python3.12-3.12.0_1   Python3 interpreter
ii lib32-glibc-2.38_1    GNU C Library (32-bit)
`
	names := parseXBPS(raw)
	want := map[string]bool{
		"bash":        true,
		"python3.12":  true,
		"lib32-glibc": true,
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected package name: %q", n)
		}
		delete(want, n)
	}
	for n := range want {
		t.Errorf("missing expected package name: %q", n)
	}
}

func TestXbpsPkgName(t *testing.T) {
	cases := []struct {
		pkgver string
		want   string
	}{
		{"bash-5.2.032_1", "bash"},
		{"python3.12-3.12.0_1", "python3.12"},
		{"lib32-glibc-2.38_1", "lib32-glibc"},
		{"go-1.21.0_1", "go"},
	}
	for _, tc := range cases {
		got := xbpsPkgName(tc.pkgver)
		if got != tc.want {
			t.Errorf("xbpsPkgName(%q) = %q, want %q", tc.pkgver, got, tc.want)
		}
	}
}

func TestSplitLines(t *testing.T) {
	raw := "neovim\ngit\n\n  vim  \n"
	got := splitLines(raw)
	if len(got) != 3 {
		t.Fatalf("want 3 lines, got %d: %v", len(got), got)
	}
	if got[2] != "vim" {
		t.Errorf("want vim, got %q", got[2])
	}
}
