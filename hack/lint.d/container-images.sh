#!/usr/bin/env bash
# container-images flavour: hadolint (.hadolint.yaml) over every Dockerfile and Containerfile.

# shellcheck source=hack/tools.sh
source "$(dirname "${BASH_SOURCE[0]}")/../tools.sh"

require_tools hadolint

cd "${REPO_ROOT}"

mapfile -d '' dockerfiles < <(repo_files '*Dockerfile' '*Containerfile' '*.Dockerfile' '*.Containerfile')
if ((${#dockerfiles[@]} > 0)); then
  log "hadolint"
  hadolint "${dockerfiles[@]}"
fi
