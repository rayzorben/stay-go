#!/usr/bin/env bash
# Toggle the dropdown terminal: bring the verterm window to the current
# workspace and focus it, or park it on "Scratch" if it's already here.
# Bound to Mod+grave (SUPER+`) in binds.kdl.
#
# verterm is only ever run as this dropdown, so it needs no --class tagging the
# way the old ghostty-based version did — the app-id "verterm" is the instance.
set -euo pipefail

APP_ID="verterm"
HIDE_WORKSPACE="Scratch"   # named parking slot declared in rules.kdl

win=$(niri msg -j windows | jq -c --arg app "$APP_ID" '[.[] | select(.app_id == $app)] | first')

if [ "$win" = "null" ]; then
    # Not running (first use, or it was closed) — spawn it. The window-rule in
    # rules.kdl opens it floating, 80%x80%, 85% opaque, on "Scratch"; run the
    # toggle again to pull it to the current workspace.
    setsid verterm >/dev/null 2>&1 &
    exit 0
fi

win_id=$(jq -r '.id' <<<"$win")
win_ws_id=$(jq -r '.workspace_id' <<<"$win")

# move-window-to-workspace takes an index or a name, never an id — so resolve
# the focused workspace to a reference we can actually pass. Named workspaces
# use their name (stable); unnamed ones fall back to their index.
focused=$(niri msg -j workspaces | jq -c '.[] | select(.is_focused)')
focused_ws_id=$(jq -r '.id' <<<"$focused")
focused_ref=$(jq -r 'if .name != null then .name else (.idx|tostring) end' <<<"$focused")

if [ "$win_ws_id" = "$focused_ws_id" ]; then
    niri msg action move-window-to-workspace --window-id "$win_id" --focus false "$HIDE_WORKSPACE"
else
    # --focus only follows a window that was already focused, and this one is
    # parked off-screen — so move first, then focus it explicitly. The move is
    # async; focusing immediately races it and lands on the wrong workspace.
    niri msg action move-window-to-workspace --window-id "$win_id" --focus false "$focused_ref"
    for _ in 1 2 3 4 5 6 7 8 9 10; do
        sleep 0.05
        if [ "$(niri msg -j windows | jq -r --arg id "$win_id" \
                '.[] | select(.id == ($id|tonumber)) | .workspace_id')" = "$focused_ws_id" ]; then
            break
        fi
    done
    niri msg action focus-window --id "$win_id"
fi
