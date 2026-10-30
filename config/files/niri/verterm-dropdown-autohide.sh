#!/usr/bin/env bash
# Auto-park the verterm dropdown when it loses focus to another window on the
# same workspace (e.g. you click a window behind it). Companion to
# verterm-dropdown-toggle.sh; started once per session from spawn.kdl.
#
# Only a focus change *within* the dropdown's workspace parks it — switching
# workspaces, or focus moving to a layer-shell surface (launcher, bar, notification;
# reported as a null window id) leaves it where it is.
set -uo pipefail

APP_ID="verterm"
HIDE_WORKSPACE="Scratch"   # same parking slot as the toggle script

on_focus_change() {
    local prev=$1 new=$2 windows dd dd_id dd_ws new_ws scratch_id
    windows=$(niri msg -j windows) || return

    dd=$(jq -c --arg app "$APP_ID" '[.[] | select(.app_id == $app)] | first' <<<"$windows")
    [ "$dd" = "null" ] && return
    dd_id=$(jq -r '.id' <<<"$dd")
    [ "$prev" = "$dd_id" ] || return     # the dropdown wasn't the one that lost focus

    dd_ws=$(jq -r '.workspace_id' <<<"$dd")
    new_ws=$(jq -r --arg id "$new" '.[] | select(.id == ($id|tonumber)) | .workspace_id' <<<"$windows")
    [ "$dd_ws" = "$new_ws" ] || return   # focus left the workspace — not a click-away

    scratch_id=$(niri msg -j workspaces | jq -r --arg n "$HIDE_WORKSPACE" '.[] | select(.name == $n) | .id')
    [ "$dd_ws" = "$scratch_id" ] && return   # already parked

    niri msg action move-window-to-workspace --window-id "$dd_id" --focus false "$HIDE_WORKSPACE"
}

while [ -n "${NIRI_SOCKET:-}" ]; do
    prev=""
    while read -r new; do
        # null = focus went to a layer-shell surface or nowhere; keep the last
        # real window as "prev" so returning from a launcher isn't a click-away.
        [ "$new" = "null" ] && continue
        [ -n "$prev" ] && [ "$new" != "$prev" ] && on_focus_change "$prev" "$new"
        prev=$new
    done < <(niri msg -j event-stream | jq --unbuffered -r '
        # The stream opens with a WindowsChanged snapshot (seeds the current
        # focus), then reports each change as WindowFocusChanged.
        if .WindowFocusChanged then .WindowFocusChanged.id
        elif .WindowsChanged then ([.WindowsChanged.windows[] | select(.is_focused) | .id] | first)
        else empty end')
    sleep 1   # event stream dropped (niri reload/restart) — reconnect
done
