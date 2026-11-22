#!/usr/bin/env bash
# Shared shell setup sourced by every other script in hack/:
#
#   source "$(dirname "${BASH_SOURCE[0]}")/tools.sh"
#
# Tool versions are not pinned here: the nix flake (flake.nix + flake.lock) is the single source
# of truth, and every script expects to run inside its dev shell (`nix develop`, or
# `nix develop -c just <recipe>`).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export REPO_ROOT

# Progress message on stderr.
log() {
  printf '==> %s\n' "$*" >&2
}

# Print an error and exit non-zero.
die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

# Fail unless every named tool is on PATH.
require_tools() {
  local missing=() tool
  for tool in "$@"; do
    command -v "${tool}" >/dev/null 2>&1 || missing+=("${tool}")
  done
  if ((${#missing[@]} > 0)); then
    die "missing tools: ${missing[*]} (run inside the dev shell: nix develop)"
  fi
}

# Files tracked or not yet added (but not ignored) that match the given pathspecs, NUL-separated.
repo_files() {
  git -C "${REPO_ROOT}" ls-files -z --cached --others --exclude-standard -- "$@"
}
