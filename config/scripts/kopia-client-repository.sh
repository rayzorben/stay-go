#!/bin/bash
# Connect this laptop's Kopia to orohost's repository server. Runs as root
# (sudo: true): kopia-backup.timer runs as root, and the connection lives in
# root's /var/lib/kopia/repository.config.
#
# The repository is orohost's, in Google Drive; orohost's Kopia server carries
# every request to it over gRPC, on the tailnet, with orohost's real ts.net
# certificate. Each laptop logs in as root@<its hostname> (the login and its
# retention are set up on orohost: config/hosts/orohost/backups.yaml and
# config/scripts/kopia-policies.sh), so it reads and writes only its own
# snapshots, and maintenance is the server's job. A reinstall connects to the
# same login, so `kopia-backup restore` sees every earlier snapshot.
#
# Safe to re-run: `repository connect` just rewrites the local connection.
set -euo pipefail

host=$(hostname)

env=/etc/kopia/kopia.env
if [[ ! -r $env ]]; then
  echo "$env is missing" >&2
  exit 1
fi
# An undefined ${secrets.*} reference is written through verbatim.
if grep -q 'secrets\.kopia\.' "$env"; then
  echo "$env still references an undefined secret — fill in secrets.kopia in config/hosts/$host/backups.yaml" >&2
  exit 1
fi

install -d -m 0700 /var/lib/kopia

kopia-root repository connect server \
  --url=https://orohost.tawny-tailor.ts.net:51515 \
  --override-username=root \
  --override-hostname="$host" \
  --description="$host"

kopia-root repository status
