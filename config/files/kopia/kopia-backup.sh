#!/usr/bin/env bash
# kopia-backup — snapshot cachygram's backup set into its Kopia repository on
# orohost, or restore it (managed by stay-go — config/hosts/cachygram/backups.yaml).
#
#   kopia-backup              snapshot everything in /etc/kopia/backup-set.conf
#                             (kopia-backup.timer runs this daily)
#   kopia-backup list         every snapshot in the repository
#   kopia-backup restore      put the newest snapshot of every source back where
#                             it came from (stop Life first: systemctl --user stop life)
#   kopia-backup restore <path>...   the same, for just these sources
#
# Retention (7 daily, 4 weekly, 12 monthly) is the repository's global policy,
# set by config/scripts/kopia-cachygram-repository.sh; Kopia applies it after
# every snapshot.
#
# A folder holding SQLite databases, or with ignore patterns, is snapshotted
# from a staged copy under $STAGE: rsync for the files, sqlite3 .backup for
# each database. --override-source records the snapshot under the real path,
# so list and restore never see the staging folder.
set -uo pipefail

[ "$(id -u)" -eq 0 ] || exec sudo "$0" "$@"

CONF=/etc/kopia/backup-set.conf
STAGE=/home/.kopia-stage # same filesystem as the data, outside snapper's reach
kopia() { /usr/local/bin/kopia-root "$@"; }
identity="$(id -un)@$(uname -n)" # what Kopia records as the source owner

# Print the sources in $CONF, one "path<TAB>ignore1<TAB>ignore2..." per line.
sources() {
	local kind root name arg words prunes
	while read -r kind rest; do
		case $kind in
		'' | '#'*) continue ;;
		path)
			read -ra words <<<"$rest"
			local out=${words[0]} i
			for ((i = 1; i < ${#words[@]}; i++)); do
				[ "${words[i]}" = ignore ] && out+=$'\t'${words[++i]}
			done
			printf '%s\n' "$out"
			;;
		find)
			read -ra words <<<"$rest"
			root=${words[0]} name=${words[1]} prunes=()
			[ -d "$root" ] || continue
			for ((i = 2; i < ${#words[@]}; i++)); do
				[ "${words[i]}" = prune ] && prunes+=(-path "${words[++i]}" -prune -o)
			done
			# -prune after a match: a .claude inside a .claude is part of it.
			find "$root" -xdev "${prunes[@]}" -type d -name "$name" -print -prune 2>/dev/null
			;;
		*) echo "kopia-backup: unknown entry in $CONF: $kind $rest" >&2 ;;
		esac
	done <"$CONF"
}

is_sqlite() { [ "$(head -c 15 "$1" 2>/dev/null)" = "SQLite format 3" ]; }

# Stage $1 into $2 with consistent database copies; $3... are rsync excludes.
stage() {
	local src=$1 dst=$2 db rel excludes=()
	shift 2
	for p in "$@"; do excludes+=(--exclude "$p"); done
	local dbs=()
	while IFS= read -r -d '' db; do is_sqlite "$db" && dbs+=("$db"); done < <(
		find "$src" -type f \( -name '*.db' -o -name '*.sqlite' -o -name '*.sqlite3' \) -print0)
	for db in "${dbs[@]}"; do
		rel=${db#"$src"/}
		excludes+=(--exclude "/$rel" --exclude "/$rel-wal" --exclude "/$rel-shm" --exclude "/$rel-journal")
	done
	mkdir -p "$dst"
	# 24 = some files vanished mid-copy: normal for live app data.
	rsync -a --delete --delete-excluded "${excludes[@]}" "$src/" "$dst/"
	local rc=$?
	[ $rc -eq 0 ] || [ $rc -eq 24 ] || return 1
	for db in "${dbs[@]}"; do
		rel=${db#"$src"/}
		mkdir -p "$(dirname "$dst/$rel")"
		rm -f "$dst/$rel"
		# A fresh mtime on purpose: a database whose changes still sit in its
		# -wal keeps the old mtime, and Kopia would skip the copy as unchanged.
		sqlite3 "$db" ".backup '${dst//\'/\'\'}/${rel//\'/\'\'}'" || return 1
	done
}

needs_stage() { # $1 = folder, $2 = number of ignore patterns
	[ "$2" -gt 0 ] && return 0
	local db
	while IFS= read -r -d '' db; do is_sqlite "$db" && return 0; done < <(
		find "$1" -type f \( -name '*.db' -o -name '*.sqlite' -o -name '*.sqlite3' \) -print0)
	return 1
}

backup() {
	[ -r "$CONF" ] || { echo "kopia-backup: $CONF is missing" >&2; exit 1; }
	local failed=0 line src ignores
	install -d -m 0700 "$STAGE"
	while IFS= read -r line; do
		IFS=$'\t' read -ra ignores <<<"$line"
		src=${ignores[0]}
		ignores=("${ignores[@]:1}")
		if [ ! -d "$src" ]; then
			echo "skip $src (not present)"
			continue
		fi
		echo "== $src"
		if needs_stage "$src" "${#ignores[@]}"; then
			local dst=$STAGE/${src#/}
			if ! stage "$src" "$dst" "${ignores[@]}"; then
				echo "kopia-backup: staging $src failed" >&2
				failed=1
				continue
			fi
			kopia snapshot create "$dst" --override-source="$identity:$src" || failed=1
		else
			kopia snapshot create "$src" || failed=1
		fi
	done < <(sources)
	exit $failed
}

# The newest snapshot id of each of this host's sources: "path<TAB>id".
latest() {
	kopia snapshot list --all --json |
		jq -r --arg u "$(id -un)" --arg h "$(uname -n)" '
			[.[] | select(.source.userName == $u and .source.host == $h)]
			| group_by(.source.path)[] | max_by(.startTime)
			| "\(.source.path)\t\(.id)"'
}

restore() {
	local failed=0 path id
	while IFS=$'\t' read -r path id; do
		if [ $# -gt 0 ] && ! printf '%s\n' "$@" | grep -qxF "$path"; then continue; fi
		echo "== restore $path ($id)"
		kopia restore "$id" "$path" || failed=1
	done < <(latest)
	exit $failed
}

case ${1:-} in
'') backup ;;
list) kopia snapshot list --all ;;
restore) shift; restore "$@" ;;
*) echo "usage: kopia-backup [list | restore [path...]]" >&2; exit 2 ;;
esac
