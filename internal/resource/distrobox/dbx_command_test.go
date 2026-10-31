package distrobox

import "testing"

func TestLacksGroup(t *testing.T) {
	cases := []struct {
		name     string
		memberOf []string
		procGids []int
		egid     int
		want     bool
	}{
		{"added mid-run", []string{"1000", "958"}, []int{1000, 998}, 1000, true},
		{"already in session", []string{"1000", "958"}, []int{1000, 958}, 1000, false},
		{"egid is docker", []string{"958"}, nil, 958, false},
		{"not a member", []string{"1000"}, []int{1000}, 1000, false},
	}
	for _, c := range cases {
		if got := lacksGroup("958", c.memberOf, c.procGids, c.egid); got != c.want {
			t.Errorf("%s: lacksGroup = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestShellJoin(t *testing.T) {
	got := shellJoin([]string{"distrobox", "create", "--home", "/home/distrobox/my box", "it's"})
	want := `distrobox create --home '/home/distrobox/my box' 'it'\''s'`
	if got != want {
		t.Errorf("shellJoin = %q, want %q", got, want)
	}
}
