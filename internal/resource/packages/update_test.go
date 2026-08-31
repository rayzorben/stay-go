package packages

import (
	"reflect"
	"testing"
)

func TestUpgradeArgsWithIgnoreFlags(t *testing.T) {
	mgr := &PackageManager{
		Name:       "paru",
		UpgradeCmd: []string{"paru", "-Syu", "--noconfirm"},
		IgnoreFmt:  "--ignore=%s",
	}
	got := upgradeArgs(mgr, []string{"linux", "frigate-helper"})
	want := []string{"paru", "-Syu", "--noconfirm", "--ignore=linux", "--ignore=frigate-helper"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("upgradeArgs = %v, want %v", got, want)
	}
}

func TestUpgradeArgsWithoutIgnoreSupport(t *testing.T) {
	mgr := &PackageManager{
		Name:       "apt-get",
		UpgradeCmd: []string{"apt-get", "upgrade", "-y"},
	}
	got := upgradeArgs(mgr, []string{"linux"})
	want := []string{"apt-get", "upgrade", "-y"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("upgradeArgs = %v, want %v (locked packages must not leak into the command)", got, want)
	}
}

func TestUpgradeArgsDoesNotMutateManagerTable(t *testing.T) {
	mgr := &PackageManager{
		UpgradeCmd: []string{"pacman", "-Syu", "--noconfirm"},
		IgnoreFmt:  "--ignore=%s",
	}
	upgradeArgs(mgr, []string{"linux"})
	if !reflect.DeepEqual(mgr.UpgradeCmd, []string{"pacman", "-Syu", "--noconfirm"}) {
		t.Errorf("UpgradeCmd mutated: %v", mgr.UpgradeCmd)
	}
}

func TestAllManagersDefineUpgradeCmd(t *testing.T) {
	for _, m := range managers {
		if len(m.UpgradeCmd) == 0 {
			t.Errorf("manager %q has no UpgradeCmd — --update=packages would silently do nothing", m.Name)
		}
	}
}
