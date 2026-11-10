#!/usr/bin/env bash
# firefox-user-js.sh — install a user.js into the default profile of Firefox
# or Zen (both use the same profiles.ini layout).
#
# The profile directory is resolved here, at execute time, from profiles.ini.
# It used to be a $(...) in a files: target, which stay-go expands when the
# config loads — on a fresh install that is before the browser has ever run, so
# the target collapsed to ~/.config/mozilla/firefox//user.js.
#
# Usage:
#   firefox-user-js.sh [--check] [firefox|zen]
#     (no flag)  copy the browser's user.js into its default profile
#     --check    exit 1 if the profile's user.js differs (read-only; stay-go
#                runs this at plan time to decide whether to run)
#   Browser defaults to firefox.
#     firefox: files/config/firefox-user.js → ~/.config/mozilla/firefox
#     zen:     files/config/zen-user.js     → ~/.config/zen
#
# No profile yet is not an error: the copy is a no-op and --check passes, so
# the first stay-go run after the browser creates its profile picks it up.

set -euo pipefail

check=0
if [[ ${1:-} == --check ]]; then check=1; shift; fi
browser=${1:-firefox}

FILES="$(cd "$(dirname "$0")/.." && pwd)/files/config"
case "$browser" in
  firefox) SRC="$FILES/firefox-user.js"; BASE="$HOME/.config/mozilla/firefox" ;;
  zen)     SRC="$FILES/zen-user.js";     BASE="$HOME/.config/zen" ;;
  *) echo "usage: $0 [--check] [firefox|zen]" >&2; exit 2 ;;
esac
INI="$BASE/profiles.ini"

profile_dir() {
  [[ -f $INI ]] || return 1
  local p
  p=$(awk -F= '/^\[Install/ {getline; print $2; exit}' "$INI")
  [[ -n $p ]] || return 1
  [[ $p == /* ]] && echo "$p" || echo "$BASE/$p"
}

if (( check )); then
  dir=$(profile_dir) || exit 0
  cmp -s "$SRC" "$dir/user.js" || { echo "$browser user.js differs in ${dir##*/}"; exit 1; }
else
  dir=$(profile_dir) || { echo "no $browser profile yet; start $browser once and re-run"; exit 0; }
  install -m 0644 "$SRC" "$dir/user.js"
  echo "installed $browser user.js into ${dir##*/}"
fi
