#!/usr/bin/env bash
set -euo pipefail

runtime_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cache_root="${Q4D_DSH_CACHE_DIR:-${runtime_root}/.tmp/upstream}"
lock_field() {
  node --input-type=module -e 'import fs from "node:fs"; console.log(JSON.parse(fs.readFileSync(process.argv[1], "utf8")).source[process.argv[2]])' "${runtime_root}/upstream.lock.json" "$1"
}
tag="$(lock_field tag)"
[[ "${tag}" =~ ^dsh-v[0-9A-Za-z.-]+$ ]] || { echo 'Invalid DSH tag' >&2; exit 1; }
archive="${cache_root}/deepseek-harness-${tag}.tar.gz"
source_dir="${cache_root}/deepseek-harness-${tag}"
source_url="$(lock_field url)"
expected_sha256="$(lock_field sha256)"

mkdir -p "${cache_root}"
if [[ ! -f "${archive}" ]]; then
  download="$(mktemp "${cache_root}/download.XXXXXX")"
  trap 'rm -f "${download}"' EXIT
  curl --proto '=https' --tlsv1.2 --fail --location --show-error --silent "${source_url}" --output "${download}"
  mv "${download}" "${archive}"
  trap - EXIT
fi

actual_sha256="$(shasum -a 256 "${archive}" | awk '{print $1}')"
if [[ "${actual_sha256}" != "${expected_sha256}" ]]; then
  echo "DSH source checksum mismatch: expected ${expected_sha256}, got ${actual_sha256}" >&2
  exit 1
fi

# Re-extract the verified archive to compare every original file. A cached
# dependency install is reusable; edited upstream code is not silently trusted.
staging="$(mktemp -d "${TMPDIR:-${cache_root}}/extract.XXXXXX")"
trap 'rm -rf "${staging}"' EXIT
tar -xzf "${archive}" --strip-components=1 -C "${staging}"
if [[ -e "${source_dir}" ]]; then
  node "${runtime_root}/scripts/verify-source-tree.mjs" "${staging}" "${source_dir}"
else
  mv "${staging}" "${source_dir}"
  trap - EXIT
fi

lock_hash="$(shasum -a 256 "${source_dir}/pnpm-lock.yaml" | awk '{print $1}')"
[[ "${lock_hash}" == "$(lock_field lockfile_sha256)" ]] || { echo 'DSH lockfile checksum mismatch' >&2; exit 1; }

printf '%s\n' "${source_dir}"
