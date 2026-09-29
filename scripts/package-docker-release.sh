#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
[[ $# -eq 3 ]] || { echo 'Usage: package-docker-release.sh IMAGE_PREFIX TAG OUTPUT.tar.gz' >&2; exit 2; }
prefix="$1" tag="$2" output="$3"
[[ "$prefix" =~ ^[a-z0-9][a-z0-9./_-]*$ && "$prefix" == */* && "$prefix" != */ ]] || exit 2
[[ "$tag" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$ && ${#tag} -le 128 ]] || exit 2
staging="$(mktemp -d)"
trap 'rm -rf "$staging"' EXIT
mkdir -p "$staging/quant4dad/scripts" "$staging/quant4dad/deploy"
cp "$root/scripts/install.sh" "$staging/quant4dad/scripts/"
cp "$root/deploy/compose.yaml" "$staging/quant4dad/deploy/"
cp "$root/LICENSE" "$staging/quant4dad/"
chmod 755 "$staging/quant4dad/scripts/install.sh"
for component in api web mcp agent; do
  key="$(printf '%s' "$component" | tr '[:lower:]' '[:upper:]')"
  printf 'Q4D_%s_IMAGE=%s-%s:%s\n' "$key" "$prefix" "$component" "$tag"
done > "$staging/quant4dad/deploy/images.env"
printf '%s\n' 'Run: ./scripts/install.sh --pull' \
  'Optional: add --with-mcp and/or --with-agent.' \
  'Web: http://127.0.0.1:3000' \
  'Login token: data/standalone/config/login-token' \
  'Back up data/standalone and the Docker API data volume before upgrading.' \
  'Docs: https://github.com/FredrickUnderwood/Quant4Dad-OpenSource#readme' \
  > "$staging/quant4dad/README.txt"
COPYFILE_DISABLE=1 tar -czf "$output" -C "$staging" quant4dad
printf 'Created %s\n' "$output"
