#!/usr/bin/env bash
# Every linter the repo runs, then each script in hack/lint.d/. CI runs this through `just lint`,
# and so does the prek hook.

# shellcheck source=hack/tools.sh
source "$(dirname "${BASH_SOURCE[0]}")/tools.sh"

require_tools git yamllint shellcheck nixfmt actionlint zizmor

cd "${REPO_ROOT}"

log "yamllint"
yamllint --strict .

log "shellcheck"
mapfile -d '' shell_files < <(repo_files '*.sh')
if ((${#shell_files[@]} > 0)); then
  shellcheck -x "${shell_files[@]}"
fi

log "nixfmt"
mapfile -d '' nix_files < <(repo_files '*.nix')
if ((${#nix_files[@]} > 0)); then
  nixfmt --check "${nix_files[@]}"
fi

# Flavour lint steps (hack/lint.d/<flavour>.sh), and any the project adds there.
for step in hack/lint.d/*.sh; do
  [ -e "${step}" ] || continue
  log "${step}"
  bash "${step}"
done

if [ -d .github/workflows ]; then
  log "actionlint"
  actionlint

  log "zizmor"
  # Offline: the online audits (known-vulnerable versions, impostor commits) need a token and
  # network; Renovate keeps the pinned actions current. Any finding fails the run.
  zizmor --offline --min-severity low .github/workflows
fi
