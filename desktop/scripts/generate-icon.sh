#!/bin/bash
set -euo pipefail

desktop_dir="$(cd "$(dirname "$0")/.." && pwd)"
icon_dir="$desktop_dir/src-tauri/icons"
icon_tmp="$(mktemp -d /private/tmp/dockyard-icon.XXXXXX)"
trap 'rm -rf "$icon_tmp"' EXIT
export CLANG_MODULE_CACHE_PATH="$icon_tmp/module-cache"
export SWIFT_MODULE_CACHE_PATH="$icon_tmp/module-cache"

swift "$desktop_dir/scripts/generate-icon.swift" \
  "$desktop_dir/src/assets/dockyard.png" "$icon_dir/icon.png"

(cd "$desktop_dir" && npm run tauri -- icon src-tauri/icons/icon.png -o "$icon_tmp/generated" >/dev/null)
cp "$icon_tmp/generated/icon.icns" "$icon_dir/icon.icns"
