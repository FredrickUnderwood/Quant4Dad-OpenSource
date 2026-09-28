#!/usr/bin/env bash
set -euo pipefail
unset NODE_OPTIONS NODE_PATH
runtime_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[[ "$#" == 3 ]] || { echo 'agent_inventory_usage' >&2; exit 1; }
cache_root="${Q4D_DSH_CACHE_DIR:-${runtime_root}/.tmp/upstream}"
[[ -f "${cache_root}/deepseek-harness-dsh-v0.1.2-alpha.5.tar.gz" ]] || { echo 'agent_inventory_source_missing' >&2; exit 1; }
source_dir="$("${runtime_root}/scripts/fetch-dsh-source.sh")"
loader="$(node --input-type=module -e 'import {createRequire} from "node:module";import {join} from "node:path";process.stdout.write(createRequire(join(process.argv[1], "package.json")).resolve("tsx/esm"))' "${source_dir}")"
exec env -i PATH="${PATH}" DSH_TELEMETRY_DISABLED=1 TSX_TSCONFIG_PATH="${source_dir}/tsconfig.json" \
  "$(node -p 'process.execPath')" --import "${loader}" "${runtime_root}/src/operations/inspect-sessions.ts" "$@"
