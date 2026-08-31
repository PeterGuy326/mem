#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
validator="${repo_root}/scripts/validate_release_action_pins.sh"
workflow_path="${repo_root}/.github/workflows/release.yml"

if ! (
  enable -n mapfile 2>/dev/null || :
  enable -n readarray 2>/dev/null || :

  # The sourced validator invokes this deterministic replacement.
  # The sourced validator calls this shell function indirectly, which static
  # analysis cannot follow across the source boundary.
  # shellcheck disable=SC2317,SC2329
  git() {
    if [[ "$#" -ne 2 ]]; then
      return 64
    fi
    if [[ "$1" != 'ls-remote' || "$2" != https://github.com/*.git ]]; then
      return 64
    fi

    local mock_action
    mock_action="${2#https://github.com/}"
    mock_action="${mock_action%.git}"
    sed -nE \
      's|^[[:space:]]*uses:[[:space:]]+([^@[:space:]#]+)@([^[:space:]#]+).*|\1 \2|p' \
      "${workflow_path}" |
      awk -v action="${mock_action}" \
        '$1 == action { print $2 "\trefs/heads/mock" }'
  }

  # The absolute path is derived from this checked-in script's directory.
  # shellcheck disable=SC1090
  source "${validator}"
); then
  printf 'ERROR: release pin validator requires Bash 4-only collection builtins\n' >&2
  exit 1
fi

printf 'PASS: release pin validator runs without Bash 4-only collection builtins\n'
