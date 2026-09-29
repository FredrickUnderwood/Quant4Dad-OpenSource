#!/usr/bin/env bash
set -euo pipefail
umask 077
destination="${Q4D_INSTALL_DIR:-$PWD/quant4dad}"
ref="${Q4D_RELEASE_REF:-master}"
[[ "$ref" =~ ^[a-zA-Z0-9][a-zA-Z0-9._-]*$ ]] || { echo 'Invalid release ref.' >&2; exit 2; }
[[ ! -e "$destination/.git" ]] || { echo 'In a Git checkout, run ./scripts/install.sh --pull instead.' >&2; exit 2; }
command -v curl >/dev/null || { echo 'curl is required.' >&2; exit 1; }
staging="$(mktemp -d)"
trap 'rm -rf "$staging"' EXIT
base="https://raw.githubusercontent.com/FredrickUnderwood/Quant4Dad-OpenSource/$ref"
files=(scripts/install.sh deploy/compose.yaml deploy/images.env LICENSE)
# Download every file successfully before updating an existing installation.
for file in "${files[@]}"; do
  mkdir -p "$staging/$(dirname "$file")"
  curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
    --connect-timeout 20 --max-time 120 "$base/$file" --output "$staging/$file"
done
mkdir -p "$destination/scripts" "$destination/deploy"
for file in "${files[@]}"; do cp "$staging/$file" "$destination/$file"; done
chmod 755 "$destination/scripts/install.sh"
printf 'Installation directory: %s\n' "$destination"
bash "$destination/scripts/install.sh" --pull "$@"
