#!/bin/sh
# Kopia after-snapshot-root action (managed by stay-go — config/hosts/orohost.yaml).
# Destroys the ZFS snapshot zfs-snapshot-before.sh took for this run. A failure
# here is never fatal: the next before-action sweeps up any leftover kopia-*.
set -u

src=$KOPIA_SOURCE_PATH
[ "$(findmnt -n -o FSTYPE --target "$src")" = zfs ] || exit 0

dataset=$(findmnt -n -o SOURCE --target "$src")
zfs destroy "$dataset@kopia-$KOPIA_SNAPSHOT_ID" 2>/dev/null || true
exit 0
