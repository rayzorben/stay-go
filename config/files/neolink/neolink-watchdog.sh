#!/bin/bash
# Managed by stay-go — see config/hosts/orohost/neolink-watchdog.yaml
#
# Restart neolink when it stops serving the driveway camera. After neolink
# loses its connection to the camera and reconnects, it logs "Connected and
# logged in" but never re-publishes /driveway/mainStream: RTSP clients connect
# and get nothing. Frigate's record ffmpeg then fails forever and the camera
# records nothing (2026-10-11 to 10-19). Only a neolink restart recovers it.
#
# Restart only when both hold, so turning recording off in Frigate never
# triggers it:
#   1. Frigate has written no driveway segment for $STALE seconds
#   2. Frigate's record watchdog has complained about it in that window
# Neolink is deliberately not probed: a second RTSP client gets 503 and can
# upset the session go2rtc is reading.
set -u

CAM=driveway_overview
REC=/storage/frigate/recordings/recordings
STALE=300 # seconds without a new segment
GRACE=300 # leave freshly started containers alone

running_for() { # seconds the container has been up; empty if not running
    local started
    started=$(docker inspect -f '{{if .State.Running}}{{.State.StartedAt}}{{end}}' "$1" 2>/dev/null) || return
    [ -n "$started" ] && echo $(( $(date +%s) - $(date -d "$started" +%s) ))
}

for c in frigate neolink; do
    up=$(running_for "$c")
    [ -n "$up" ] && [ "$up" -ge "$GRACE" ] || exit 0
done

# Frigate files segments under UTC date/hour; look at today and yesterday.
dirs=()
for d in "$(date -u +%F)" "$(date -u -d yesterday +%F)"; do
    [ -d "$REC/$d" ] && dirs+=("$REC/$d")
done
if [ ${#dirs[@]} -gt 0 ] && [ -n "$(find "${dirs[@]}" -path "*/$CAM/*.mp4" -newermt "-$STALE seconds" -print -quit 2>/dev/null)" ]; then
    exit 0
fi

docker logs --since "${STALE}s" frigate 2>&1 \
    | grep -q "No new valid recording segments were created for $CAM" || exit 0

echo "no $CAM segment in ${STALE}s and Frigate reports record failures; restarting neolink"
docker restart neolink
