#!/usr/bin/env bash
# dropbox-exclude.sh — mark regenerable build/cache folders as Dropbox-ignored.
#
# Dropbox skips any path carrying the xattr user.com.dropbox.ignored=1: it stops
# syncing it and deletes the cloud copy, while the local folder stays put. This
# walks the Dropbox folder and marks folders that tooling recreates (dependency
# trees, virtualenvs, compiler output, tool caches) so new projects never churn.
#
# Usage:
#   dropbox-exclude.sh           mark every candidate folder not yet ignored
#   dropbox-exclude.sh --check   list them, exit 1 if any (read-only; stay-go
#                                runs this at plan time to decide whether to run)
#   dropbox-exclude.sh --list    list them, always exit 0 (read-only)
#
# A folder qualifies when ANY of these holds:
#   1. its name is always generated (node_modules, __pycache__, .venv, ...)
#   2. venv/ or env/ that is a Python virtualenv (contains pyvenv.cfg)
#   3. it is .claude/worktrees (agent worktrees — full checkouts of the repo)
#   4. bin/obj next to a .NET project file; target/ next to Cargo.toml or pom.xml
#   5. bin/obj/out/build/dist/target that the enclosing git repo ignores
# Rules 4–5 exist because those names are only *sometimes* build output (a
# hand-written bin/ of scripts must keep syncing). Ignored folders are not
# descended into, and neither is .git.
#
# Already-ignored folders whose names are not on these lists (a hand-ignored
# data folder, say) are still walked; marks under them are suppressed.
#
# To keep a folder syncing that this would mark, put an empty .dropbox-keep file
# in it. Folders already carrying the attribute are left alone (never re-set).

set -uo pipefail

ROOT="${DROPBOX_ROOT:-$HOME/Dropbox}"
ATTR=user.com.dropbox.ignored
MODE=apply
case "${1:-}" in
  --check) MODE=check ;;
  --list)  MODE=list ;;
  "")      ;;
  *) echo "usage: $0 [--check|--list]" >&2; exit 2 ;;
esac

[[ -d $ROOT ]] || { echo "dropbox-exclude: $ROOT not found, nothing to do" >&2; exit 0; }

ALWAYS=(
  node_modules __pycache__ .venv .terraform .terragrunt-cache
  .pytest_cache .ruff_cache .mypy_cache .tox .nox .hypothesis
  .databricks .gradle .next .nuxt .svelte-kit .turbo .parcel-cache .vs
)
MAYBE=(bin obj out build dist target)

declare -A always maybe
for n in "${ALWAYS[@]}"; do always[$n]=1; done
for n in "${MAYBE[@]}";  do maybe[$n]=1;  done

has_attr() { getfattr -h --only-values -n "$ATTR" -- "$1" >/dev/null 2>&1; }

# A sibling .NET project file (bin/, obj/) or Rust/Maven manifest (target/).
sibling_manifest() {
  local dir=$1 parent=${1%/*} name=${1##*/} f
  case $name in
    bin|obj)
      for f in "$parent"/*.csproj "$parent"/*.vbproj "$parent"/*.fsproj "$parent"/*.sln; do
        [[ -e $f ]] && return 0
      done ;;
    target)
      [[ -e $parent/Cargo.toml || -e $parent/pom.xml ]] && return 0 ;;
  esac
  return 1
}

git_ignored() {
  local parent=${1%/*}
  git -C "$parent" rev-parse --is-inside-work-tree >/dev/null 2>&1 || return 1
  git -C "$parent" check-ignore -q -- "${1##*/}/" 2>/dev/null
}

qualifies() {
  local dir=$1 name=${1##*/}
  [[ -n ${always[$name]:-} ]] && return 0
  [[ ($name == venv || $name == env) && -f $dir/pyvenv.cfg ]] && return 0
  [[ $name == worktrees && ${dir%/*} == */.claude ]] && return 0
  if [[ -n ${maybe[$name]:-} ]]; then
    sibling_manifest "$dir" && return 0
    git_ignored "$dir" && return 0
  fi
  return 1
}

found=() failed=0

# True when an ancestor below $ROOT is already ignored — its contents don't sync,
# so marking anything inside it again would be noise.
ancestor_ignored() {
  local p=${1%/*}
  while [[ $p == "$ROOT"/* ]]; do
    has_attr "$p" && return 0
    p=${p%/*}
  done
  return 1
}

# find does the walk (one process, not one per directory) and stops at every
# folder whose name could qualify; only those are inspected here. A candidate
# that turns out not to qualify is scanned in turn, so nothing below it is lost.
NAMES=("${ALWAYS[@]}" "${MAYBE[@]}" venv env worktrees)
name_expr=()
for n in "${NAMES[@]}"; do name_expr+=(-o -name "$n"); done
name_expr=("${name_expr[@]:1}")

scan() {
  local d
  while IFS= read -r -d '' d; do
    has_attr "$d" && continue
    if [[ -e $d/.dropbox-keep ]] || ! qualifies "$d"; then
      scan "$d"
      continue
    fi
    ancestor_ignored "$d" || found+=("$d")
  done < <(find "$1" -mindepth 1 \( -name .git -o -name .dropbox.cache \) -prune \
             -o -type d \( "${name_expr[@]}" \) -print0 -prune 2>/dev/null)
}

scan "$ROOT"

for d in "${found[@]}"; do
  if [[ $MODE == apply ]]; then
    if setfattr -n "$ATTR" -v 1 -- "$d"; then
      echo "excluded: ${d#"$ROOT"/}"
    else
      echo "FAILED:   ${d#"$ROOT"/}" >&2
      failed=1
    fi
  else
    echo "${d#"$ROOT"/}"
  fi
done

case $MODE in
  apply) [[ ${#found[@]} -eq 0 ]] && echo "dropbox-exclude: nothing new to exclude"; exit $failed ;;
  check) [[ ${#found[@]} -eq 0 ]] ;;
  list)  exit 0 ;;
esac
