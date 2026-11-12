#!/usr/bin/env bash
# edge-profiles.sh — declare Microsoft Edge profiles and a menu launcher for each.
#
# Each NAME becomes:
#   - a profile directory <user-data-dir>/NAME, named NAME in Edge's profile
#     list (profile.info_cache in "Local State"; Edge creates the directory
#     itself the first time the profile is opened)
#   - ~/.local/share/applications/microsoft-edge-<name>.desktop, which opens
#     Edge with --profile-directory=NAME. Unquoted: names never contain
#     spaces, and some launchers (Dropbox's sign-in xdg-open) pass the
#     quotes through literally. No MimeType: these are menu entries, not
#     URL handlers; while one claimed http/https before Zen became the
#     default, Dropbox's first-run sign-in page went to it, mangled.
#
# Usage:
#   edge-profiles.sh NAME...           create/rename profiles, write launchers
#   edge-profiles.sh --check NAME...   exit 1 if anything differs (read-only;
#                                      stay-go runs this at plan time)
#
# Edge keeps "Local State" in memory and rewrites it on exit, so it is only
# edited while Edge is closed; otherwise the run fails and says so, and the
# next stay-go run retries. Launchers are always written.
#
# A name dropped from the list loses its launcher (only launchers this script
# wrote, marked X-StayGo-Edge-Profile), never its profile data.
#
# EDGE_USER_DATA_DIR overrides the user data dir (default ~/.config/microsoft-edge).

set -euo pipefail

DATA="${EDGE_USER_DATA_DIR:-$HOME/.config/microsoft-edge}"
APPS="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
BIN=/usr/bin/microsoft-edge-stable

check=0
if [[ ${1:-} == --check ]]; then check=1; shift; fi
(( $# )) || { echo "usage: $0 [--check] NAME..." >&2; exit 2; }

slug() { tr '[:upper:]' '[:lower:]' <<<"$1" | sed -E 's/[^a-z0-9]+/-/g; s/^-|-$//g'; }

launcher() { # NAME → desktop file content
  cat <<EOF
[Desktop Entry]
Version=1.0
Type=Application
Name=Edge ($1)
GenericName=Web Browser
Comment=Microsoft Edge, $1 profile
Exec=$BIN --profile-directory=$1 %U
Icon=microsoft-edge
Terminal=false
Categories=Network;WebBrowser;
StartupNotify=true
X-StayGo-Edge-Profile=$1
EOF
}

# Local State: print the profile names that are missing or misnamed; with
# --write, fix them in place (atomic replace).
local_state() { # [--write] NAME...
  python3 - "$DATA/Local State" "$@" <<'PY'
import json, os, sys
path, args = sys.argv[1], sys.argv[2:]
write = args[:1] == ["--write"]
names = args[1:] if write else args
try:
    with open(path) as f:
        st = json.load(f)
except FileNotFoundError:
    st = {}
cache = st.setdefault("profile", {}).setdefault("info_cache", {})
bad = [n for n in names
       if cache.get(n, {}).get("name") != n or cache.get(n, {}).get("is_using_default_name", True)]
for n in bad:
    print(n)
if write and bad:
    for n in bad:
        e = cache.setdefault(n, {})
        e["name"] = n
        e["is_using_default_name"] = False
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = path + ".stay-go.tmp"
    with open(tmp, "w") as f:
        json.dump(st, f, separators=(",", ":"))
    os.replace(tmp, path)
PY
}

edge_running() { pgrep -u "$(id -u)" -x msedge >/dev/null; }

# Launchers this script wrote for names no longer listed.
stale_launchers() {
  local f n keep
  for f in "$APPS"/microsoft-edge-*.desktop; do
    [[ -f $f ]] || continue
    n=$(sed -n 's/^X-StayGo-Edge-Profile=//p' "$f")
    [[ -n $n ]] || continue
    keep=0
    for want in "$@"; do [[ $n == "$want" ]] && keep=1; done
    (( keep )) || echo "$f"
  done
}

if (( check )); then
  drift=()
  while read -r n; do [[ -n $n ]] && drift+=("profile $n"); done < <(local_state "$@")
  for n in "$@"; do
    cmp -s <(launcher "$n") "$APPS/microsoft-edge-$(slug "$n").desktop" || drift+=("launcher $n")
  done
  while read -r f; do [[ -n $f ]] && drift+=("stale ${f##*/}"); done < <(stale_launchers "$@")
  (( ${#drift[@]} == 0 )) && exit 0
  echo "edge: ${drift[*]}"
  exit 1
fi

mkdir -p "$APPS"
for n in "$@"; do
  f="$APPS/microsoft-edge-$(slug "$n").desktop"
  if ! cmp -s <(launcher "$n") "$f"; then
    launcher "$n" >"$f.tmp" && mv "$f.tmp" "$f"
    echo "wrote launcher ${f##*/}"
  fi
done
while read -r f; do
  [[ -n $f ]] || continue
  rm -f "$f"
  echo "removed stale launcher ${f##*/}"
done < <(stale_launchers "$@")
command -v update-desktop-database >/dev/null && update-desktop-database "$APPS" 2>/dev/null || true

pending=$(local_state "$@")
[[ -z $pending ]] && exit 0
if edge_running; then
  echo "Edge is running; close it and re-run to set up profiles: $(tr '\n' ' ' <<<"$pending")" >&2
  exit 1
fi
local_state --write "$@" >/dev/null
echo "set up Edge profiles: $(tr '\n' ' ' <<<"$pending")"
