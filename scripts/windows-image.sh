#!/usr/bin/env bash
# Image a laptop's factory Windows disk before bootstrap.sh wipes it, keep the
# image on orohost, and put it back if ever needed. How-to: windows-image.md.
#
# The laptop boots the CachyOS live USB and runs sshd; this script runs on
# another machine (cachygram, or orohost itself) and drives it over ssh:
#
#   windows-image.sh capture <live-addr> [name]   pull the disk into ./<name>-<date>/
#   windows-image.sh push <dir>                   copy it to orohost and verify it
#   windows-image.sh restore <live-addr> <dir>    write it back over the live disk
#
#   live-addr  [user@]address of the live USB (user defaults to liveuser)
#   name       defaults to rayzorbot-windows
#   DISK=...   the internal disk on the live machine (default /dev/nvme0n1)
#
# capture saves the GPT (ptable.sfdisk), then each partition, compressed with
# zstd on the live machine before it crosses Wi-Fi: NTFS partitions through
# partclone (used blocks only), everything else (EFI, MSR) through dd. A
# BitLocker-encrypted partition stops it: partclone can't read one, and a raw
# copy would be the whole 2TB. partitions.tsv records each partition's number
# and type for restore; SHA256SUMS covers every file.
#
# The live USB's host key is new every boot, so it is neither checked nor kept.
# One ssh master connection carries everything: the password is asked once.
set -euo pipefail

DISK=${DISK:-/dev/nvme0n1}
OROHOST=${OROHOST:-rayben@orohost}
ARCHIVE=/storage/backup/archive

die() { echo "windows-image: $*" >&2; exit 1; }
usage() {
	echo "usage: windows-image.sh capture <live-addr> [name] | push <dir> | restore <live-addr> <dir>" >&2
	exit 2
}

# Remote commands are plain words and pipes, so they mean the same in the live
# user's shell whatever it is (fish on CachyOS).
connect() {
	live=$1
	[[ $live == *@* ]] || live=liveuser@$live
	ctl=$(mktemp -u "${XDG_RUNTIME_DIR:-/tmp}/windows-image-XXXXXX")
	ssh_opts=(-o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o LogLevel=ERROR
		-o ControlMaster=auto -o "ControlPath=$ctl" -o ControlPersist=600)
	trap 'ssh -o ControlPath="$ctl" -O exit "$live" 2>/dev/null || true' EXIT
	on_live true || die "can't ssh to $live — on the live USB: passwd, then sudo systemctl start sshd"
	on_live sudo -n true || die "$live has no passwordless sudo"
	on_live test -b "$DISK" || die "$DISK is not a disk on $live (set DISK=...)"
	[ "$(on_live lsblk -dno TRAN "$DISK")" != usb ] || die "$DISK is a USB disk — the live stick, not the internal one?"
}
on_live() { ssh "${ssh_opts[@]}" "$live" "$*"; }

# nvme0n1 + 3 → nvme0n1p3; sda + 3 → sda3.
part_path() { [[ $1 == *[0-9] ]] && echo "${1}p$2" || echo "$1$2"; }

capture() {
	[ $# -ge 1 ] || usage
	connect "$1"
	local dir
	dir=$PWD/${2:-rayzorbot-windows}-$(date +%Y%m%d)
	[ ! -e "$dir" ] || die "$dir already exists"

	on_live command -v partclone.ntfs >/dev/null ||
		on_live sudo pacman -Sy --noconfirm --needed partclone >/dev/null ||
		die "couldn't install partclone on $live"

	local -a parts=()
	local name fs
	while read -r name fs; do
		[ "$name" != "$DISK" ] || continue
		[ "$fs" != BitLocker ] || die "$name is BitLocker-encrypted: in Windows, save the recovery key and run manage-bde -off C: first"
		parts+=("$name ${fs:-raw}")
	done < <(on_live lsblk -lnpo NAME,FSTYPE "$DISK")
	[ ${#parts[@]} -gt 0 ] || die "$DISK has no partitions"

	mkdir -p "$dir"
	cd "$dir"
	on_live lsblk -o NAME,SIZE,FSTYPE,PARTLABEL,LABEL "$DISK" >lsblk.txt
	on_live sudo sfdisk -d "$DISK" >ptable.sfdisk
	cat lsblk.txt

	local p cmd n
	for p in "${parts[@]}"; do
		read -r name fs <<<"$p"
		n=${name##*[!0-9]}
		case $fs in
		ntfs) cmd="sudo partclone.ntfs -c -s $name -o -" ;;
		*) cmd="sudo dd if=$name bs=4M status=progress" ;;
		esac
		echo "== partition $n ($fs)"
		on_live "$cmd | zstd -T0 -3 -q" </dev/null >"p$n.img.zst.part"
		mv "p$n.img.zst.part" "p$n.img.zst"
		printf '%s\t%s\n' "$n" "$fs" >>partitions.tsv
	done
	sha256sum -- * >SHA256SUMS
	du -sh "$dir"
	echo "Captured. Next: scripts/windows-image.sh push $dir"
}

push() {
	[ $# -eq 1 ] && [ -f "$1/SHA256SUMS" ] || die "push needs a captured image folder (one with SHA256SUMS)"
	local dir name
	dir=$(cd "$1" && pwd)
	name=$(basename "$dir")
	rsync -a --info=progress2 "$dir/" "$OROHOST:$ARCHIVE/$name/"
	# orohost's login shell is fish, so the check runs in bash.
	ssh "$OROHOST" bash -s <<-EOF
		cd $ARCHIVE/$name && sha256sum -c --quiet SHA256SUMS && echo "Verified on orohost: $ARCHIVE/$name"
	EOF
	echo "It's safe to delete $dir now."
}

restore() {
	[ $# -eq 2 ] || usage
	local dir=$2
	[ -f "$dir/partitions.tsv" ] && [ -f "$dir/ptable.sfdisk" ] || die "$dir is not a captured image folder"
	(cd "$dir" && sha256sum -c --quiet SHA256SUMS) || die "$dir fails its checksums"
	connect "$1"

	on_live lsblk -o NAME,SIZE,FSTYPE,PARTLABEL,LABEL "$DISK"
	echo "This ERASES $DISK on $live and writes $dir over it."
	local answer
	read -rp "Type the disk's name ($(basename "$DISK")) to go on: " answer
	[ "$answer" = "$(basename "$DISK")" ] || die "stopped; nothing written"

	on_live command -v partclone.ntfs >/dev/null ||
		on_live sudo pacman -Sy --noconfirm --needed partclone >/dev/null ||
		die "couldn't install partclone on $live"
	on_live sudo wipefs -aq "$DISK"
	on_live sudo sfdisk -q "$DISK" <"$dir/ptable.sfdisk"
	on_live sudo partprobe "$DISK"
	on_live sudo udevadm settle

	local n fs target cmd
	while IFS=$'\t' read -r n fs; do
		target=$(part_path "$DISK" "$n")
		case $fs in
		ntfs) cmd="sudo partclone.ntfs -r -s - -o $target" ;;
		*) cmd="sudo dd of=$target bs=4M status=progress" ;;
		esac
		echo "== partition $n ($fs) → $target"
		on_live "zstd -dc -q | $cmd" <"$dir/p$n.img.zst"
	done <"$dir/partitions.tsv"
	on_live sync
	echo "Restored. Remove the USB and boot; Windows may ask for the BitLocker recovery key once."
}

case ${1:-} in
capture) shift; capture "$@" ;;
push) shift; push "$@" ;;
restore) shift; restore "$@" ;;
*) usage ;;
esac
