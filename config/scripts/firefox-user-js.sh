#!/usr/bin/env bash
# firefox-user-js.sh — install files/config/firefox-user.js into the default
# Firefox profile.
#
# The profile directory is resolved here, at execute time, from profiles.ini.
# It used to be a $(...) in a files: target, which stay-go expands when the
# config loads — on a fresh install that is before Firefox has ever run, so the
# target collapsed to ~/.config/mozilla/firefox//user.js.
#
# Usage:
#   firefox-user-js.sh           copy user.js into the default profile
#   firefox-user-js.sh --check   exit 1 if the profile's user.js differs (read-only;
#                                stay-go runs this at plan time to decide whether to run)
#
# No profile yet is not an error: the copy is a no-op and --check passes, so
# the first stay-go run after Firefox creates its profile picks it up.

set -euo pipefail

SRC="$(cd "$(dirname "$0")/.." && pwd)/files/config/firefox-user.js"
BASE="$HOME/.config/mozilla/firefox"
INI="$BASE/profiles.ini"

profile_dir() {
  [[ -f $INI ]] || return 1
  local p
  p=$(awk -F= '/^\[Install/ {getline; print $2; exit}' "$INI")
  [[ -n $p ]] || return 1
  [[ $p == /* ]] && echo "$p" || echo "$BASE/$p"
}

case "${1:-}" in
  --check)
    dir=$(profile_dir) || exit 0
    cmp -s "$SRC" "$dir/user.js" || { echo "user.js differs in ${dir##*/}"; exit 1; }
    ;;
  "")
    dir=$(profile_dir) || { echo "no Firefox profile yet; start Firefox once and re-run"; exit 0; }
    install -m 0644 "$SRC" "$dir/user.js"
    echo "installed user.js into ${dir##*/}"
    ;;
  *) echo "usage: $0 [--check]" >&2; exit 2 ;;
esac
