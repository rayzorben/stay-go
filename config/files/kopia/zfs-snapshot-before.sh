#!/bin/sh
# Kopia before-snapshot-root action (managed by stay-go — config/hosts/orohost.yaml).
#
# Backs up a frozen copy instead of the live tree. /storage/apps holds running
# databases (immich postgres, photoprism mariadb, the *arr and jellyfin sqlite
# files); copied file by file while they write, those come out torn. A ZFS
# snapshot is atomic and crash-consistent, which is exactly what a database
# recovers from on restore.
#
# Kopia passes KOPIA_SOURCE_PATH and KOPIA_SNAPSHOT_ID. Printing
# KOPIA_SNAPSHOT_PATH=<dir> makes Kopia read <dir> while still recording the
# snapshot under the original source path, so the UI shows /storage/apps.
#
# The snapshot is read through the dataset's .zfs/snapshot automount (works with
# snapdir=hidden), so nothing has to be mounted or unmounted by hand. A source
# that is not on ZFS is snapshotted live: the script prints nothing.
#
# Child datasets are not included: ZFS snapshots one dataset, and a child shows
# up empty inside its parent's snapshot. Back a child up as its own source.
set -eu

src=$KOPIA_SOURCE_PATH
[ "$(findmnt -n -o FSTYPE --target "$src")" = zfs ] || exit 0

dataset=$(findmnt -n -o SOURCE --target "$src")
mnt=$(findmnt -n -o TARGET --target "$src")

# Clear out snapshots left behind when an after-action never ran (crash,
# reboot, server killed mid-upload). Safe because the server takes one
# snapshot at a time (MaxParallelSnapshots defaults to 1).
zfs list -H -t snapshot -o name -d 1 "$dataset" | { grep "@kopia-" || true; } | while read -r old; do
  zfs destroy "$old" || true
done

snap="kopia-$KOPIA_SNAPSHOT_ID"
zfs snapshot "$dataset@$snap"
echo "KOPIA_SNAPSHOT_PATH=$mnt/.zfs/snapshot/$snap${src#"$mnt"}"
