#!/bin/sh
set -eu
# Official Wasm Labs CPython distribution, verified before extracting three
# fixed files. No model-supplied URL, archive name or extraction destination.
target=${1:-/opt/q4d/python-wasi}
archive=$(mktemp)
trap 'rm -f "$archive"' EXIT
curl --fail --location --retry 3 --connect-timeout 20 --max-time 300 \
  'https://github.com/vmware-labs/webassembly-language-runtimes/releases/download/python/3.12.0%2B20231211-040d5a6/python-3.12.0-wasi-sdk-20.0.tar.gz' -o "$archive"
expected=6c1cddbb69ae09e87eee2906bdc70539bff5f2969818a6f8457d4e6a6eb67d4d
if command -v sha256sum >/dev/null 2>&1; then actual=$(sha256sum "$archive"); else actual=$(shasum -a 256 "$archive"); fi
[ "${actual%% *}" = "$expected" ] || { echo 'Python WASI archive checksum mismatch' >&2; exit 1; }
mkdir -p "$target"
tar -xzf "$archive" -C "$target" bin/python-3.12.0.wasm usr/local/lib/python312.zip usr/local/lib/python3.12/os.py
chmod -R a+rX "$target"
