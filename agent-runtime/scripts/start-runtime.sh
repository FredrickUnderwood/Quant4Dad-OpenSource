#!/usr/bin/env bash
set -euo pipefail
unset NODE_OPTIONS NODE_PATH

runtime_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fail() { echo 'agent_runtime_launcher_failed' >&2; exit 1; }
[[ "$#" == 1 && "$1" == /* ]] || fail
node -e 'const [a,b]=process.versions.node.split(".").map(Number);process.exit(a===24&&b>=20?0:1)' || fail
[[ -n "${Q4D_AGENT_CONTROL_TOKEN_FILE:-}" && -n "${Q4D_AGENT_BRIDGE_TOKEN_FILE:-}" ]] || fail
[[ -z "${Q4D_AGENT_CONTROL_TOKEN+x}" && -z "${Q4D_AGENT_BRIDGE_TOKEN+x}" ]] || fail

# Startup is offline: provisioning runs fetch-dsh-source.sh and the frozen
# dependency installation first. Reverify cached archive/source/lock every boot.
tag="$(node --input-type=module -e 'import fs from "node:fs";process.stdout.write(JSON.parse(fs.readFileSync(process.argv[1])).source.tag)' "${runtime_root}/upstream.lock.json")"
cache_root="${Q4D_DSH_CACHE_DIR:-${runtime_root}/.tmp/upstream}"
[[ -f "${cache_root}/deepseek-harness-${tag}.tar.gz" && -d "${cache_root}/deepseek-harness-${tag}" ]] || fail
source_dir="$("${runtime_root}/scripts/fetch-dsh-source.sh" 2>/dev/null)" || fail
loader="$(node --input-type=module -e 'import {createRequire} from "node:module";import {join} from "node:path";process.stdout.write(createRequire(join(process.argv[1], "package.json")).resolve("tsx/esm"))' "${source_dir}" 2>/dev/null)" || fail
node_bin="$(node -p 'process.execPath')"

# exec preserves SIGTERM/SIGINT delivery and exit status. Ambient API keys,
# proxies, NODE_OPTIONS and DSH profile/plugin configuration do not reach DSH.
exec env -i PATH="${PATH}" DSH_TELEMETRY_DISABLED=1 TSX_TSCONFIG_PATH="${source_dir}/tsconfig.json" \
  Q4D_AGENT_CONTROL_TOKEN_FILE="${Q4D_AGENT_CONTROL_TOKEN_FILE}" \
  Q4D_AGENT_BRIDGE_TOKEN_FILE="${Q4D_AGENT_BRIDGE_TOKEN_FILE}" \
  "${node_bin}" --import "${loader}" "${runtime_root}/src/runtime/main.ts" "$1"
