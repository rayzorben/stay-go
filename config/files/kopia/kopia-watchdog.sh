#!/bin/bash
# Managed by stay-go — see config/hosts/orohost/backups.yaml
#
# Restart kopia-server when backups have stopped. The server reaches Google
# Drive through an rclone child it starts itself. On 2026-10-17 that rclone
# began dropping requests and from 10-20 answered none; Kopia neither exits nor
# restarts it, its scheduler stops scheduling snapshots after the failed one,
# and systemd still sees a running service. No backups ran 10-20 to 10-27.
# Only a server restart, which starts a fresh rclone, recovers it.
#
# Unhealthy, once the server has been up for $GRACE seconds, when:
#   1. the control API does not answer within 20 s, or
#   2. a source's last snapshot is older than $STALE and it is not uploading, or
#   3. a source is uploading but its progress has not moved for $STUCK seconds.
set -u

STALE=$((2 * 3600)) # sources snapshot every 15 min
STUCK=$((30 * 60))
GRACE=$((60 * 60))  # time to open the repository and finish a first upload pass
STATE=/run/kopia-watchdog
API=http://127.0.0.1:51515/api/v1/control/sources

systemctl -q is-active kopia-server || exit 0
since=$(systemctl show -P ActiveEnterTimestamp kopia-server)
[ $(( $(date +%s) - $(date -d "$since" +%s) )) -ge "$GRACE" ] || exit 0

restart() {
    echo "$1; restarting kopia-server"
    rm -rf "$STATE"
    systemctl restart kopia-server
    exit 0
}

set -a
. /etc/kopia/kopia.env
set +a
# Credentials on stdin rather than argv, where ps would show them.
json=$(printf 'user = "%s:%s"\n' "$KOPIA_SERVER_CONTROL_USER" "$KOPIA_SERVER_CONTROL_PASSWORD" \
    | curl -sSf -m 20 -K - "$API") || restart "the control API did not answer"

mkdir -p "$STATE"
now=$(date +%s)
while IFS=$'\t' read -r path status ended progress; do
    ended_s=0
    [ -n "$ended" ] && ended_s=$(date -d "$ended" +%s)
    mark="$STATE/${path//\//_}"

    if [ "$status" != UPLOADING ]; then
        rm -f "$mark"
        [ $((now - ended_s)) -lt "$STALE" ] \
            || restart "$path: no snapshot since ${ended:-ever} and none running"
        continue
    fi

    # Uploading: remember when the counters last moved.
    read -r last_progress last_moved 2>/dev/null <"$mark" || last_progress=
    if [ "$progress" != "$last_progress" ]; then
        echo "$progress $now" >"$mark"
    elif [ $((now - last_moved)) -ge "$STUCK" ]; then
        restart "$path: upload has made no progress for $((STUCK / 60)) min"
    fi
done < <(jq -r '.sources[]? | [
    .source.path,
    .status,
    (.lastSnapshot.endTime // ""),
    ((.upload // {}) | [.hashedBytes, .cachedBytes, .uploadedBytes, .hashedFiles, .cachedFiles] | map(. // 0) | add)
] | @tsv' <<<"$json")
