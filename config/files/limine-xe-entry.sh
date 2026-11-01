#!/usr/bin/env bash
# Managed by stay-go. limine-entry-tool post-hook (/etc/boot/hooks/post.d/).
#
# Adds a second top-level boot menu entry, "Arch Linux (xe driver)", that boots
# the same kernels with the Intel xe DRM driver instead of i915. Choosing it or
# the regular "Arch Linux" entry at the Limine menu switches drivers per boot.
#
# limine-entry-tool has no way to give one kernel two cmdlines (per-kernel boot
# profiles are only a planned feature), so this hook copies every kernel entry
# of this machine's OS entry (found by its machine-id comment) after each
# limine.conf update. path/module_path keep their verification hashes, so the
# copy is always in step with the kernels and initramfs just written. The
# cmdline is the regular one with every i915.* parameter swapped for the xe ones.
#
# Runs before 90-limine-enroll-config, so an enrolled config checksum covers it.
# With --remove, strips the entry and adds nothing.
set -euo pipefail

CONF=${LIMINE_CONF:-/boot/limine.conf}
OS_NAME="Arch Linux (xe driver)"
PCI_ID=7d55 # Meteor Lake-P Arc Graphics; xe still needs force_probe for it
XE_PARAMS="i915.force_probe=!$PCI_ID xe.force_probe=$PCI_ID xe.enable_psr=0"

[[ -f $CONF ]] || exit 0
machine_id=$(</etc/machine-id)

tmp=$(mktemp "$CONF.xe.XXXXXX")
trap 'rm -f "$tmp"' EXIT

awk -v name="$OS_NAME" -v mid="$machine_id" -v params="$XE_PARAMS" \
	-v remove="$([[ ${1:-} == --remove ]] && echo 1)" '
function xe_cmdline(cmd,   n, i, t, out) {
	n = split(cmd, t, " ")
	out = ""
	for (i = 1; i <= n; i++)
		if (t[i] !~ /^(i915|xe)\./) out = out (out == "" ? "" : " ") t[i]
	return out " " params
}
# Emit the managed entry from the kernels collected out of the OS entry.
function emit(   i, j) {
	if (remove || nk == 0) return
	print "/+" name
	print "comment: Managed by stay-go. Same kernels as Arch Linux, booted with the xe driver instead of i915."
	for (i = 1; i <= nk; i++) {
		print "  //" kname[i]
		for (j = 1; j <= nopt[i]; j++) print "  " opt[i, j]
		print ""
	}
	nk = 0
}
# A top-level entry: one slash, optionally followed by + (expanded).
/^\/[^\/]/ {
	if (in_os) emit()
	in_os = 0; cur = 0
	hdr = $0; sub(/^\/\+?/, "", hdr)
	skip = (hdr == name)
	if (!skip) print
	next
}
skip { next }
# The OS entry is ours when its own (unindented) comment names this machine-id.
/^comment:/ && index($0, "machine-id=" mid) { in_os = 1 }
in_os && /^[ \t]*\/\/[^\/]/ {
	e = $0; sub(/^[ \t]*\/\/\+?/, "", e)
	cur = 0
	if (e != "Snapshots") { cur = ++nk; kname[cur] = e; nopt[cur] = 0 }
}
in_os && /^[ \t]*\/\/\// { cur = 0 }
in_os && cur && /^[ \t]*[a-z_]+:/ && !/^[ \t]*comment:/ {
	line = $0; sub(/^[ \t]+/, "", line)
	if (line ~ /^cmdline:/) { c = line; sub(/^cmdline: */, "", c); line = "cmdline: " xe_cmdline(c) }
	opt[cur, ++nopt[cur]] = line
}
{ print }
END { if (in_os) emit() }
' "$CONF" >"$tmp"

cmp -s "$tmp" "$CONF" || mv -f "$tmp" "$CONF"
