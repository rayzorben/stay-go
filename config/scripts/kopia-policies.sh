#!/bin/bash
# What orohost backs up to Google Drive, how often, and for how long.
# Runs as root (sudo: true). Edit this file and re-apply: stay-go re-runs it
# whenever its content changes. Policies changed in the Kopia UI survive until
# the next time this script runs, which puts them back to what is written here.
#
# Adding a folder: add a `backup` line at the bottom. The server picks a source
# up as soon as it has a policy, so no snapshot has to be started by hand.
set -euo pipefail

set -a
. /etc/kopia/kopia.env
set +a

# control_api — GET the server's source list as the control user, with the
# credentials on stdin rather than argv (ps shows argv to every user).
control_api() {
  printf 'user = "%s:%s"\n' "$KOPIA_SERVER_CONTROL_USER" "$KOPIA_SERVER_CONTROL_PASSWORD" |
    curl -sSf -o /dev/null -K - "$@" http://127.0.0.1:51515/api/v1/control/sources
}

# A wedged server (see /usr/local/bin/kopia-watchdog) never answers the refresh
# at the bottom, so stop now rather than after ~15 min of policy calls. The
# retries cover a server restarted moments ago by `kopia server reload`.
if systemctl -q is-active kopia-server &&
  ! control_api -m 20 --retry 6 --retry-delay 10 --retry-connrefused; then
  echo "kopia-server is running but not answering: sudo systemctl restart kopia-server, then re-apply" >&2
  exit 1
fi

# ── Every source: retention, compression ──────────────────────────────────────
# Tiered retention. A snapshot is kept if ANY rule still wants it:
#   every snapshot     for 48 h    (192 × 15 min)
#   one per hour       for 30 days
#   one per day        for a year
#   one per month      effectively forever (100 years)
# Dedup means a kept snapshot costs only the blocks no other snapshot shares.
kopia-root policy set --global \
  --keep-latest=192 \
  --keep-hourly=720 \
  --keep-daily=365 \
  --keep-weekly=0 \
  --keep-monthly=1200 \
  --keep-annual=0 \
  --compression=zstd \
  --ignore-cache-dirs=true

# Already-compressed formats: zstd would burn CPU for nothing.
kopia-root policy set --global --clear-never-compress
kopia-root policy set --global \
  --add-never-compress=.jpg --add-never-compress=.jpeg --add-never-compress=.png \
  --add-never-compress=.heic --add-never-compress=.webp --add-never-compress=.avif \
  --add-never-compress=.mp4 --add-never-compress=.mov --add-never-compress=.mkv \
  --add-never-compress=.webm --add-never-compress=.mp3 --add-never-compress=.flac \
  --add-never-compress=.zip --add-never-compress=.gz --add-never-compress=.zst \
  --add-never-compress=.xz --add-never-compress=.7z --add-never-compress=.bz2

# backup PATH [IGNORE...]
#   Snapshot PATH every 15 minutes through a ZFS snapshot (see
#   /etc/kopia/zfs-snapshot-before.sh). IGNORE patterns are gitignore-style,
#   relative to PATH; a leading / anchors one to PATH itself.
backup() {
  local path=$1
  shift
  # Ignores before the schedule, so a brand-new source is never snapshotted
  # without them. --clear-ignore returns before applying any --add-ignore in
  # the same call, so clearing and re-adding take two calls.
  kopia-root policy set "$path" --clear-ignore
  if (($#)); then
    local args=() pattern
    for pattern in "$@"; do args+=(--add-ignore="$pattern"); done
    kopia-root policy set "$path" "${args[@]}"
  fi
  kopia-root policy set "$path" \
    --snapshot-interval=15m \
    --before-snapshot-root-action=/etc/kopia/zfs-snapshot-before.sh \
    --after-snapshot-root-action=/etc/kopia/zfs-snapshot-after.sh
}

# ── Sources ───────────────────────────────────────────────────────────────────
# /storage/apps: every container's config and database (~570 GB on disk, about
# 250 GB once the regenerable caches below are left out). Immich's and
# PhotoPrism's originals live in Dropbox, not here; what is here is their
# databases, DB dumps and sidecars, which is what a rebuild actually needs.
backup /storage/apps \
  /immich/library/thumbs \
  /immich/library/encoded-video \
  /photoprism/cache \
  /photoprism/storage/cache \
  /jellyfin/cache \
  /qbittorrent/downloads \
  /code-server/.cache \
  "/plex/config/Library/Application Support/Plex Media Server/Cache" \
  "/plex/config/Library/Application Support/Plex Media Server/Codecs" \
  "/plex/config/Library/Application Support/Plex Media Server/Crash Reports" \
  "/plex/config/Library/Application Support/Plex Media Server/Diagnostics" \
  "/plex/config/Library/Application Support/Plex Media Server/Drivers" \
  "/plex/config/Library/Application Support/Plex Media Server/Logs" \
  "/plex/config/Library/Application Support/Plex Media Server/Media" \
  "/plex/config/Library/Application Support/Plex Media Server/Metadata" \
  "/plex/config/Library/Application Support/Plex Media Server/Updates"

# /storage/backup: Home Assistant backups and the photos-dupes working set
# (~230 GB, nearly all photos-dupes/Photos). The Python venv is rebuilt with pip.
backup /storage/backup \
  /.venv

# Have a running server see new or changed sources now rather than at its next
# 4-hourly refresh. Not running yet (first apply) is fine: it reads them on start.
# The password goes in through the environment, not --server-control-password.
if systemctl -q is-active kopia-server; then
  KOPIA_SERVER_PASSWORD=$KOPIA_SERVER_CONTROL_PASSWORD timeout 60 \
    kopia server refresh --server-control-username="$KOPIA_SERVER_CONTROL_USER" ||
    echo "kopia server refresh failed; the server picks the policies up at its next refresh" >&2
fi
