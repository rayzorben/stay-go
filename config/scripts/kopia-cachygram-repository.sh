#!/bin/bash
# Connect cachygram's Kopia to its repository on orohost, creating it on the
# very first run, and set the retention policy. Runs as root (sudo: true):
# kopia-backup.timer runs as root, and the connection lives in root's
# /var/lib/kopia/repository.config.
#
# Storage is SFTP to orohost over the tailnet, as rayben, with ~rayben/.ssh/id_rsa
# (restored from stay-go secrets and already in orohost's authorized_keys), into
# /storage/backup/kopia-cachygram. A reinstall connects to the same repository,
# so `kopia-backup restore` sees every earlier snapshot.
#
# Safe to re-run: `repository connect` just rewrites the local connection, and
# `repository create` refuses a location that already holds a repository, so a
# failed connect (bad password, orohost unreachable) can never overwrite data.
set -euo pipefail

env=/etc/kopia/kopia.env
if [[ ! -r $env ]]; then
  echo "$env is missing" >&2
  exit 1
fi
# An undefined ${secrets.*} reference is written through verbatim.
if grep -q 'secrets\.kopia\.' "$env"; then
  echo "$env still references an undefined secret — fill in secrets.kopia in config/hosts/cachygram/backups.yaml" >&2
  exit 1
fi
for f in /etc/kopia/known_hosts /home/rayben/.ssh/id_rsa; do
  [[ -r $f ]] || { echo "$f is missing" >&2; exit 1; }
done

install -d -m 0700 /var/lib/kopia

storage=(
  sftp
  --host=orohost
  --username=rayben
  --path=/storage/backup/kopia-cachygram
  --keyfile=/home/rayben/.ssh/id_rsa
  --known-hosts=/etc/kopia/known_hosts
)
connect=(
  --no-check-for-updates # pacman owns the kopia version
  --description=cachygram
)

if kopia-root repository connect "${storage[@]}" "${connect[@]}"; then
  echo "connected to the existing repository"
else
  echo "no repository to connect to — creating one"
  kopia-root repository create "${storage[@]}" "${connect[@]}"
fi

# Retention: a week of dailies, a month of weeklies, a year of monthlies, and
# nothing else. keep-latest 1 only guarantees the newest snapshot survives;
# it is always also that day's daily.
kopia-root policy set --global \
  --keep-latest=1 --keep-hourly=0 --keep-daily=7 --keep-weekly=4 \
  --keep-monthly=12 --keep-annual=0 \
  --compression=zstd

# Quick and full maintenance (which actually frees deleted snapshots' space)
# run from `snapshot create` on the owner's machine. After a reinstall the
# owner is still root@cachygram, but claim it explicitly in case it moved.
kopia-root maintenance set --owner=me

kopia-root repository status
