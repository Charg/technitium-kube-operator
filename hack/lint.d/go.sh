#!/usr/bin/env bash
# go flavour: golangci-lint's linters and formatters (.golangci.yml) over every Go module.
# GOLANGCI_LINT overrides the binary, e.g. a custom build with module plugins.

# shellcheck source=hack/tools.sh
source "$(dirname "${BASH_SOURCE[0]}")/../tools.sh"

golangci_lint="${GOLANGCI_LINT:-golangci-lint}"
require_tools go "${golangci_lint}"

cd "${REPO_ROOT}"

mapfile -d '' modules < <(repo_files 'go.mod' '*/go.mod')
for module in "${modules[@]}"; do
  dir="$(dirname "${module}")"
  log "golangci-lint run (${dir})"
  (cd "${dir}" && "${golangci_lint}" run ./...)
  log "golangci-lint fmt --diff (${dir})"
  (cd "${dir}" && "${golangci_lint}" fmt --diff ./...)
done
