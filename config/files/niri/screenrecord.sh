#!/bin/bash
# Shift+Alt+Print: toggle a screen recording of the focused output.
# Press once to start, again to stop; saves to ~/Videos/Recordings.
if pkill -INT -x wf-recorder; then
  notify-send -a "Screen recording" -i media-record "Recording saved" "$(cat "${XDG_RUNTIME_DIR}/screenrecord.path" 2>/dev/null)"
  exit 0
fi
dir=~/Videos/Recordings; mkdir -p "$dir"
out="$dir/$(date +%Y%m%d-%H%M%S).mp4"
output=$(niri msg -j focused-output | sed -n 's/.*"name":"\([^"]*\)".*/\1/p')
echo "$out" > "${XDG_RUNTIME_DIR}/screenrecord.path"
notify-send -a "Screen recording" -i media-record "Recording started" "Shift+Alt+Print again to stop"
exec wf-recorder ${output:+-o "$output"} -f "$out"
