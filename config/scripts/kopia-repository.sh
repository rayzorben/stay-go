#!/bin/bash
# Connect orohost's Kopia to its Google Drive repository, creating the repository
# on the very first run. Runs as root (sudo: true) — the server does too, and the
# connection lives in root's /var/lib/kopia/repository.config.
#
# Safe to re-run: `repository connect` just rewrites the local connection, and
# `repository create` refuses a location that already holds a repository, so a
# failed connect (bad password, Drive unreachable) can never overwrite data.
set -euo pipefail

for f in /etc/kopia/kopia.env /etc/kopia/rclone.conf; do
  if [[ ! -r $f ]]; then
    echo "$f is missing" >&2
    exit 1
  fi
  # An undefined ${secrets.*} reference is written through verbatim.
  if grep -q 'secrets\.kopia\.' "$f"; then
    echo "$f still references an undefined secret — uncomment and fill in secrets.kopia in config/hosts/orohost/backups.yaml" >&2
    exit 1
  fi
done

# Kopia creates its cache and log directories, but the connection file goes
# straight into KOPIA_CONFIG_PATH's directory.
install -d -m 0700 /var/lib/kopia

storage=(
  rclone
  --remote-path=gdrive:Backups/kopia-orohost
  --rclone-exe=/usr/bin/rclone
  --rclone-args=--config=/etc/kopia/rclone.conf
  # Kopia writes ~20 MB pack blobs; one 64M chunk uploads each in one request
  # instead of three (rclone's default is 8M).
  --rclone-args=--drive-chunk-size=64M
  # Google caps uploads at 750 GB/day per account. Fail fast when that is hit
  # rather than retrying for hours; the next 15-minute run resumes the upload.
  --rclone-args=--drive-stop-on-upload-limit
  # Log rclone's own output into the server log. Kopia otherwise reads it only
  # for rclone's ports and drops it, which left the October rclone hang with no
  # cause on record. (Hidden flag; costs about one stats line a second.)
  --rclone-debug
)
connect=(
  --enable-actions        # the ZFS before/after snapshot actions
  --no-check-for-updates  # pacman owns the kopia version
  --description=orohost
)

if kopia-root repository connect "${storage[@]}" "${connect[@]}"; then
  echo "connected to the existing repository"
else
  echo "no repository to connect to — creating one"
  kopia-root repository create "${storage[@]}" "${connect[@]}"
fi
kopia-root repository status
