#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
docker build --platform linux/amd64 --file Dockerfile.build --output bin/linux .
install -d desktop/src-tauri/resources/ubuntu
install -m 0755 bin/linux/dockyard bin/linux/dockyardctl desktop/src-tauri/resources/ubuntu/
