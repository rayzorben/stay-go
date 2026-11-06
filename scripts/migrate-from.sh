#!/usr/bin/env bash
# Move a laptop's data onto the laptop that replaces it: every folder its Kopia
# backup set names (config/files/kopia/laptop-backup-set.conf — Life's and the
# mail app's data, the keyrings, the .claude folders, Wi-Fi and VPN profiles,
# the CIFS credentials), straight from the old machine over the tailnet, so the
# new one gets the state of this minute rather than of the last snapshot.
# Everything else arrives by itself: stay-go rebuilds the system, Dropbox the
# files.
#
# After bootstrap.sh and a stay-go run on the new laptop:
#   on the old one   scripts/migrate-from.sh --serve
#   on the new one   scripts/migrate-from.sh <old-host> [-n] [path...]
#                    -n    list what would change; stops and changes nothing
#                    path  extra folders to bring along
#   on the old one   scripts/migrate-from.sh --stop   (only if a copy never finished)
#
# Neither laptop runs sshd, so --serve starts one for the move: a transient
# unit (migrate-sshd) listening on the tailnet address only, port 2222, keys
# only, accepting just the old laptop's own public keys, which the new laptop
# has too (secrets.ssh). Its config and host key live in /run; the new side
# stops it when done, and it stops itself after 4 hours regardless.
#
# The copy:
#   - stops Life (and Aurora, and Life's nightly backup) on both laptops first,
#     so their databases are copied at rest. On the old laptop they stay
#     stopped: its data has moved, and its nightly backup would otherwise
#     overwrite the new laptop's in orohost's nightly folders.
#   - merges into what is already here; every file it replaces is kept under
#     /var/backups/migrate-<time>/ (root, 0700: it holds secrets).
#   - reloads NetworkManager's profiles and starts Life again here.
#
# Close Claude Code on the old laptop first (its ~/.claude is being copied).
# Afterwards log out and back in, so the keyring daemon reads the copied
# keyrings; they unlock at login only if the password matches the old laptop's.
set -euo pipefail

PORT=2222
RUN=/run/migrate-sshd
UNIT=migrate-sshd
LIFE=(life.service life-aurora.service life-backup.timer)
here=$(cd "$(dirname "$0")" && pwd)

die() { echo "migrate-from: $*" >&2; exit 1; }

serve() {
	local ip
	ip=$(tailscale ip -4 2>/dev/null) || die "tailscale is not up"
	if systemctl is-active -q "$UNIT"; then
		echo "Already serving $(hostname) on $ip:$PORT"
		return
	fi
	compgen -G "$HOME/.ssh/*.pub" >/dev/null || die "no public keys in ~/.ssh"
	sudo rm -rf "$RUN"
	sudo install -d -m 0755 "$RUN"
	sudo ssh-keygen -q -t ed25519 -N '' -C "$UNIT@$(hostname)" -f "$RUN/host_key"
	sed 's|^|from="100.64.0.0/10" |' "$HOME"/.ssh/*.pub | sudo tee "$RUN/authorized_keys" >/dev/null
	sudo tee "$RUN/sshd_config" >/dev/null <<-EOF
		ListenAddress $ip:$PORT
		HostKey $RUN/host_key
		AuthorizedKeysFile $RUN/authorized_keys
		AllowUsers $USER
		PermitRootLogin no
		PasswordAuthentication no
		KbdInteractiveAuthentication no
		UsePAM yes
	EOF
	sudo /usr/bin/sshd -t -f "$RUN/sshd_config"
	sudo systemd-run -q --unit="$UNIT" -p RuntimeMaxSec=4h -p ExecStopPost="/usr/bin/rm -rf $RUN" \
		/usr/bin/sshd -D -e -f "$RUN/sshd_config"
	echo "Serving $(hostname) on $ip:$PORT for up to 4 hours."
	echo "Close Claude Code here, then on the new laptop run: scripts/migrate-from.sh $(hostname)"
}

case ${1:-} in
--serve) serve; exit ;;
--stop) sudo systemctl stop "$UNIT"; exit ;;
'' | -*) echo "usage: migrate-from.sh <old-host> [-n] [path...] | --serve | --stop" >&2; exit 2 ;;
esac

src=$1
shift
dry=()
extra=()
for a in "$@"; do
	case $a in
	-n | --dry-run) dry=(--dry-run) ;;
	/*) extra+=("${a%/}") ;;
	*) die "extra paths must be absolute: $a" ;;
	esac
done
[ "$src" != "$(hostname)" ] || [ ${#dry[@]} -gt 0 ] || die "$src is this machine"

# The tailnet authenticates both ends, and the host key is a throwaway made by
# --serve, so there is nothing to pin and no reason to keep it in known_hosts.
ssh_opts=(-p "$PORT" -o BatchMode=yes -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no
	-o LogLevel=ERROR -o ControlMaster=auto -o "ControlPath=$XDG_RUNTIME_DIR/migrate-%C" -o ControlPersist=300)
on_src() { ssh "${ssh_opts[@]}" "$USER@$src" "$@"; }
# rsync runs as root here (it writes /etc and other users' files) but connects
# as $USER, with $USER's agent, over the same master connection.
rsh="sudo -u $USER SSH_AUTH_SOCK=$SSH_AUTH_SOCK ssh ${ssh_opts[*]}"

on_src true 2>/dev/null || die "can't reach $src:$PORT — run scripts/migrate-from.sh --serve on $src first"

# The old laptop's own backup set, as its kopia-backup would read it, using
# this repo's copy of the script so the old laptop needs no update first.
mapfile -t lines < <(on_src sudo bash -s sources <"$here/../config/files/kopia/kopia-backup.sh")
[ ${#lines[@]} -gt 0 ] || die "$src's backup set names no folders"
lines+=("${extra[@]}")

if [ ${#dry[@]} -eq 0 ]; then
	echo "Stopping Life on $src and here"
	on_src systemctl --user stop "${LIFE[@]}"
	systemctl --user stop "${LIFE[@]}"
	# Started again however the copy ends: a half-merged Life is still better
	# running than not, and a rerun stops it again.
	trap 'systemctl --user start life.service life-backup.timer' EXIT
	bk=/var/backups/migrate-$(date +%Y%m%d-%H%M%S)
	sudo install -d -m 0700 "$bk"
fi

failed=0
for line in "${lines[@]}"; do
	IFS=$'\t' read -ra f <<<"$line"
	path=${f[0]}
	excludes=()
	for p in "${f[@]:1}"; do excludes+=(--exclude "$p"); done
	if ! on_src sudo test -d "$(printf %q "$path")"; then
		echo "skip $path (not on $src)"
		continue
	fi
	echo "== $path"
	backup=()
	[ ${#dry[@]} -gt 0 ] || backup=(--backup --backup-dir="$bk$path")
	# 24 = some files vanished mid-copy: normal for live app data.
	rc=0
	sudo rsync -aHAX --numeric-ids --mkpath --stats -h "${dry[@]}" "${backup[@]}" "${excludes[@]}" \
		-e "$rsh" --rsync-path="sudo rsync" "$src:$path/" "$path/" |
		grep -E '^(Number of regular files transferred|Total transferred file size)' || rc=${PIPESTATUS[0]}
	if [ "$rc" -ne 0 ] && [ "$rc" -ne 24 ]; then
		echo "migrate-from: copying $path failed (rsync exit $rc)" >&2
		failed=1
	fi
done

if [ ${#dry[@]} -gt 0 ]; then
	echo "Dry run: nothing was stopped or changed."
	exit $failed
fi

sudo nmcli connection reload
# Stopping the unit ends this connection too, so don't wait on it.
on_src sudo systemctl stop --no-block "$UNIT" 2>/dev/null || true
ssh "${ssh_opts[@]}" -O exit "$USER@$src" 2>/dev/null || true

if [ $failed -ne 0 ]; then
	echo "Some folders failed; rerun after scripts/migrate-from.sh --serve on $src."
	echo "Life on $src is stopped: systemctl --user start life.service there to go back to it."
	exit 1
fi
echo "Done. Replaced files are in $bk."
echo "Life stays stopped on $src. Log out and back in here to load the copied keyrings."
