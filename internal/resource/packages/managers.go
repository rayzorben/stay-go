package packages

import (
	"context"
	"fmt"
	"strings"

	"github.com/rayzorben/stay-go/internal/executor"
)

// PackageManager defines all the commands needed to manage packages for a
// specific package manager. The table is searched in order; the first entry
// whose Binary is available in PATH is used.
type PackageManager struct {
	Name       string
	Binary     string
	NeedsSudo  bool
	ListCmd    []string
	InstallCmd []string
	RemoveCmd  []string
	// Env contains extra environment variables injected into every command
	// (e.g. DEBIAN_FRONTEND=noninteractive for apt-get).
	Env []string
	// UpdateCmd, when non-nil, triggers a per-install-package index sync plan
	// node (e.g. "apt-get update") after that package's depends and before
	// install. Managers that embed sync in install (pacman -Sy, paru -Sy)
	// leave this nil.
	UpdateCmd []string
	// UpgradeCmd performs a full system upgrade (--update mode). Managers whose
	// upgrade needs a fresh index first rely on UpdateCmd running before it.
	UpgradeCmd []string
	// IgnoreFmt, when non-empty, is the per-package exclusion flag appended to
	// UpgradeCmd for entries with lock: true (e.g. "--ignore=%s", "--exclude=%s").
	// Empty means the manager has no per-run exclusion; locked packages may
	// still be upgraded and the plan says so.
	IgnoreFmt string
	// MarkExplicitCmd, when non-nil, records the given packages as explicitly
	// installed (pacman -D --asexplicit, apt-mark manual, apk add → world).
	// It runs before every removal for all still-configured installed packages,
	// because RemoveCmd also drops dependencies that are no longer needed
	// (pacman -Rs, apt --autoremove, apk del) — and a configured package that
	// was first pulled in as a dependency is still marked as one. Managers
	// whose RemoveCmd never takes unrequested packages leave this nil.
	MarkExplicitCmd []string
	// ResolveCmd, when non-nil, maps configured names to installed database
	// names before MarkExplicitCmd (pacman -Qq resolves provides, e.g.
	// zen-browser → zen-browser-bin). Its stdout is used even on a non-zero
	// exit, which only means some names were not found.
	ResolveCmd []string
	// ParseOutput optionally post-processes the raw list output.
	// nil means treat each non-empty line as a package name.
	ParseOutput func(raw string) []string
}

// usesPacmanQqList reports whether m lists installed packages via pacman -Qq.
// On Arch, that output uses database package names (e.g. zen-browser-bin) while
// config and paru -S often use a virtual / AUR name (e.g. zen-browser) that
// pacman -Q <name> still resolves via provides.
func usesPacmanQqList(m *PackageManager) bool {
	if m == nil || len(m.ListCmd) < 2 {
		return false
	}
	return m.ListCmd[0] == "pacman" && m.ListCmd[1] == "-Qq"
}

// managers is the ordered preference table. Detection walks this list and
// returns the first whose Binary exists in PATH.
var managers = []PackageManager{
	// ── Arch Linux (AUR helpers first) ──────────────────────────────────────
	{
		Name:       "paru",
		Binary:     "paru",
		NeedsSudo:  false, // paru escalates internally when needed
		ListCmd:    []string{"pacman", "-Qq"},
		InstallCmd: []string{"paru", "-Sy", "--noconfirm", "--needed"},
		RemoveCmd:  []string{"paru", "-Rs", "--noconfirm"},
		// -Rs drops orphaned deps; keep configured ones by marking them explicit.
		MarkExplicitCmd: []string{"paru", "-D", "--asexplicit"},
		ResolveCmd:      []string{"pacman", "-Qq"},
		UpgradeCmd:      []string{"paru", "-Syu", "--noconfirm"},
		IgnoreFmt:       "--ignore=%s",
	},
	{
		Name:       "yay",
		Binary:     "yay",
		NeedsSudo:  false,
		ListCmd:    []string{"pacman", "-Qq"},
		InstallCmd: []string{"yay", "-Sy", "--noconfirm", "--needed"},
		RemoveCmd:  []string{"yay", "-Rs", "--noconfirm"},
		// -Rs drops orphaned deps; keep configured ones by marking them explicit.
		MarkExplicitCmd: []string{"yay", "-D", "--asexplicit"},
		ResolveCmd:      []string{"pacman", "-Qq"},
		UpgradeCmd:      []string{"yay", "-Syu", "--noconfirm"},
		IgnoreFmt:       "--ignore=%s",
	},
	{
		Name:       "pacman",
		Binary:     "pacman",
		NeedsSudo:  true,
		ListCmd:    []string{"pacman", "-Qq"},
		InstallCmd: []string{"pacman", "-Sy", "--noconfirm", "--needed"},
		RemoveCmd:  []string{"pacman", "-Rs", "--noconfirm"},
		// -Rs drops orphaned deps; keep configured ones by marking them explicit.
		MarkExplicitCmd: []string{"pacman", "-D", "--asexplicit"},
		ResolveCmd:      []string{"pacman", "-Qq"},
		UpgradeCmd:      []string{"pacman", "-Syu", "--noconfirm"},
		IgnoreFmt:       "--ignore=%s",
	},
	// ── Debian / Ubuntu ──────────────────────────────────────────────────────
	{
		Name:       "apt-get",
		Binary:     "apt-get",
		NeedsSudo:  true,
		ListCmd:    []string{"dpkg-query", "-f", "${Package}\\n", "-W"},
		UpdateCmd:  []string{"apt-get", "update", "-y"},
		InstallCmd: []string{"apt-get", "install", "-y"},
		RemoveCmd:  []string{"apt-get", "remove", "-y", "--autoremove"},
		// --autoremove drops every auto-installed package nothing depends on.
		MarkExplicitCmd: []string{"apt-mark", "manual"},
		UpgradeCmd:      []string{"apt-get", "upgrade", "-y"},
		Env:             []string{"DEBIAN_FRONTEND=noninteractive"},
	},
	// ── Fedora / RHEL ─────────────────────────────────────────────────────────
	{
		Name:       "dnf",
		Binary:     "dnf",
		NeedsSudo:  true,
		ListCmd:    []string{"rpm", "-qa", "--queryformat", "%{NAME}\\n"},
		InstallCmd: []string{"dnf", "install", "-y"},
		// dnf removes unneeded deps by default (clean_requirements_on_remove);
		// its mark verb differs between dnf4 and dnf5, so turn cleanup off.
		RemoveCmd:  []string{"dnf", "remove", "-y", "--setopt=clean_requirements_on_remove=False"},
		UpgradeCmd: []string{"dnf", "upgrade", "-y"},
		IgnoreFmt:  "--exclude=%s",
	},
	// ── CentOS / older RHEL ───────────────────────────────────────────────────
	{
		Name:       "yum",
		Binary:     "yum",
		NeedsSudo:  true,
		ListCmd:    []string{"rpm", "-qa", "--queryformat", "%{NAME}\\n"},
		InstallCmd: []string{"yum", "install", "-y"},
		RemoveCmd:  []string{"yum", "remove", "-y"},
		UpgradeCmd: []string{"yum", "update", "-y"},
		IgnoreFmt:  "--exclude=%s",
	},
	// ── openSUSE ──────────────────────────────────────────────────────────────
	{
		Name:       "zypper",
		Binary:     "zypper",
		NeedsSudo:  true,
		ListCmd:    []string{"rpm", "-qa", "--queryformat", "%{NAME}\\n"},
		UpdateCmd:  []string{"zypper", "refresh"},
		InstallCmd: []string{"zypper", "install", "-y", "--no-confirm"},
		RemoveCmd:  []string{"zypper", "remove", "-y"},
		UpgradeCmd: []string{"zypper", "update", "-y"},
	},
	// ── Alpine Linux ──────────────────────────────────────────────────────────
	{
		Name:       "apk",
		Binary:     "apk",
		NeedsSudo:  true,
		ListCmd:    []string{"apk", "info"},
		UpdateCmd:  []string{"apk", "update"},
		InstallCmd: []string{"apk", "add"},
		RemoveCmd:  []string{"apk", "del"},
		// apk del drops deps no longer required by world; add puts a tracked
		// (dependency-installed) package into world without reinstalling it.
		MarkExplicitCmd: []string{"apk", "add"},
		UpgradeCmd:      []string{"apk", "upgrade"},
	},
	// ── Void Linux ────────────────────────────────────────────────────────────
	{
		Name:        "xbps-install",
		Binary:      "xbps-install",
		NeedsSudo:   true,
		ListCmd:     []string{"xbps-query", "-l"},
		UpdateCmd:   []string{"xbps-install", "-S"},
		InstallCmd:  []string{"xbps-install", "-y"},
		RemoveCmd:   []string{"xbps-remove", "-y"},
		UpgradeCmd:  []string{"xbps-install", "-yu"},
		ParseOutput: parseXBPS,
	},
}

// betterManagerNowAvailable reports whether a higher-priority manager than
// current has become available in PATH. Used after a successful install to
// detect when e.g. paru was just installed and should now be used.
func betterManagerNowAvailable(current *PackageManager) bool {
	for i := range managers {
		if managers[i].Name == current.Name {
			return false // reached current without finding anything better
		}
		if executor.Exists(managers[i].Binary) {
			return true
		}
	}
	return false
}

// Detect walks the managers table and returns the first whose Binary is found
// in PATH. Returns an error listing supported managers if none is detected.
func Detect(_ context.Context) (*PackageManager, error) {
	for i := range managers {
		if executor.Exists(managers[i].Binary) {
			return &managers[i], nil
		}
	}
	var names []string
	for _, m := range managers {
		names = append(names, m.Name)
	}
	return nil, fmt.Errorf("no supported package manager found (looked for: %s)", strings.Join(names, ", "))
}

// parseXBPS converts `xbps-query -l` output into plain package names.
// Each line has the form: "ii pkgname-version description"
// The version always starts with a digit, so we split on the last "-digit"
// sequence to extract the name.
func parseXBPS(raw string) []string {
	var names []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Fields: state pkgver description...
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pkgver := fields[1] // e.g. "bash-5.2.032_1"
		names = append(names, xbpsPkgName(pkgver))
	}
	return names
}

// xbpsPkgName extracts the package name from a pkgver string like "bash-5.2_1".
// Package names can contain hyphens; versions always start with a digit after
// the last separating hyphen.
func xbpsPkgName(pkgver string) string {
	parts := strings.Split(pkgver, "-")
	// Find the last part that starts with a digit — that's the version boundary.
	for i := len(parts) - 1; i > 0; i-- {
		if len(parts[i]) > 0 && parts[i][0] >= '0' && parts[i][0] <= '9' {
			return strings.Join(parts[:i], "-")
		}
	}
	return pkgver // fallback: return as-is
}
