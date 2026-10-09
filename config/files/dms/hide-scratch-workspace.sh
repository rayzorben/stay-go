#!/usr/bin/env bash
# Hide the "Scratch" workspace from the DMS bar's workspace switcher.
#
# DMS has no setting for this: its niri workspace filter (getNiriWorkspaces in
# WorkspaceSwitcher.qml) only knows "is it active" and "does it hold a window".
# Scratch permanently holds the parked dropdown terminal, so it always counts
# as occupied and always renders — even with showOccupiedWorkspacesOnly=true.
#
# The file is owned by the dms-shell package, so every `pacman -Syu` reverts
# this. That's why a pacman hook re-runs it (see dms-hide-scratch.hook) and why
# it must be safe to run any number of times.
#
# Quickshell reads these .qml files from disk at startup (`qs -p
# /usr/share/quickshell/dms`) — nothing is precompiled — so the patch takes
# effect on `systemctl --user restart dms`. It is NOT live: DMS runs with
# DMS_DISABLE_HOT_RELOAD=1.
set -euo pipefail

QML="/usr/share/quickshell/dms/Modules/DankBar/Widgets/WorkspaceSwitcher.qml"
WORKSPACE="Scratch"
MARKER="stay-go:hide-scratch"

# The line the insertion hangs off — the point where the niri workspace list has
# been gathered and sorted, but not yet filtered. Patching here (rather than in
# the showOccupiedWorkspacesOnly branch below it) hides Scratch regardless of
# that setting.
ANCHOR='workspaces = workspaces.slice().sort((a, b) => a.idx - b.idx);'

[ -f "$QML" ] || { echo "dms-hide-scratch: $QML not found; dms-shell not installed?" >&2; exit 0; }

# Already patched — nothing to do. This is the idempotency guard, and also what
# makes the pacman hook cheap on upgrades that didn't touch the file.
if grep -qF "$MARKER" "$QML"; then
    echo "dms-hide-scratch: already patched"
    exit 0
fi

# If upstream reworks this function the anchor disappears. Fail loudly rather
# than silently leaving Scratch visible, so the patch gets revisited.
if [ "$(grep -cF "$ANCHOR" "$QML")" != "1" ]; then
    echo "dms-hide-scratch: anchor not found exactly once in $QML — upstream changed, patch needs review" >&2
    exit 1
fi

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

# awk rather than sed: the anchor is full of (), => and . that would all need
# escaping in a regex, and a literal string comparison can't drift.
awk -v anchor="$ANCHOR" -v ws="$WORKSPACE" -v marker="$MARKER" '
    { print }
    index($0, anchor) && !done {
        match($0, /^[ \t]*/)
        indent = substr($0, 1, RLENGTH)
        printf "%sworkspaces = workspaces.filter(ws => ws.name !== \"%s\"); // %s\n", indent, ws, marker
        done = 1
    }
' "$QML" > "$tmp"

install -m 644 "$tmp" "$QML"
echo "dms-hide-scratch: patched $QML (restart dms to apply)"
