#!/usr/bin/env bash
set -euo pipefail

runtime_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_dir="$("${runtime_root}/scripts/fetch-dsh-source.sh")"
pnpm_bin="${runtime_root}/node_modules/.bin/pnpm"
store_dir="${Q4D_DSH_STORE_DIR:-${runtime_root}/.tmp/pnpm-store}"

if [[ ! -x "${pnpm_bin}" ]]; then
  echo "The pinned pnpm installer is missing; run npm ci in ${runtime_root}" >&2
  exit 1
fi

# Avoid auto-downgrading to the vulnerable installer declared by alpha.5.
# The dependency graph still comes exclusively from the upstream frozen lock.
"${pnpm_bin}" --dir "${source_dir}" install --frozen-lockfile --ignore-scripts \
  --pm-on-fail=ignore --registry https://registry.npmjs.org \
  --store-dir "${store_dir}"
"${runtime_root}/scripts/fetch-dsh-source.sh" >/dev/null
Q4D_DSH_SOURCE_DIR="${source_dir}" node "${runtime_root}/scripts/check-bootstrap-types.mjs"
Q4D_DSH_SOURCE_DIR="${source_dir}" node "${runtime_root}/scripts/generate-model-defaults.mjs" --check

if [[ "${Q4D_TEST_UPSTREAM_DEFAULT_PROFILE:-0}" == "1" ]]; then
  "${pnpm_bin}" --dir "${source_dir}" --pm-on-fail=ignore exec vitest run \
    --config vitest.e2e.config.ts --retry 0 \
    apps/cli/tests/profiles/acp/tests/control-surface.e2e.ts
fi

cd "${runtime_root}"
Q4D_DSH_SOURCE_DIR="${source_dir}" npm run test:control
