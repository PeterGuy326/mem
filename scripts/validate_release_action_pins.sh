#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
workflow="${repo_root}/.github/workflows/release.yml"

mapfile -t pins < <(
  sed -nE \
    's|^[[:space:]]*uses:[[:space:]]+([^@[:space:]#]+)@([^[:space:]#]+).*|\1 \2|p' \
    "${workflow}" | sort -u
)

if [[ "${#pins[@]}" -eq 0 ]]; then
  printf 'ERROR: no action pins found in %s\n' "${workflow}" >&2
  exit 1
fi

for pin in "${pins[@]}"; do
  read -r action revision <<< "${pin}"
  if [[ ! "${revision}" =~ ^[0-9a-f]{40}$ ]]; then
    printf 'ERROR: %s uses non-immutable revision %s\n' \
      "${action}" "${revision}" >&2
    exit 1
  fi

  remote="https://github.com/${action}.git"
  refs=''
  for attempt in 1 2 3; do
    if refs="$(git ls-remote "${remote}")"; then
      break
    fi
    if [[ "${attempt}" -eq 3 ]]; then
      printf 'ERROR: unable to resolve action repository %s\n' "${remote}" >&2
      exit 1
    fi
  done

  if ! awk -v revision="${revision}" \
    '$1 == revision { found = 1 } END { exit(found ? 0 : 1) }' <<< "${refs}"; then
    printf 'ERROR: %s revision %s is not advertised by %s\n' \
      "${action}" "${revision}" "${remote}" >&2
    exit 1
  fi
done

printf 'PASS: release workflow action pins resolve to official repositories\n'
