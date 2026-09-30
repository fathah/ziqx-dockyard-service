#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
version=$(cat VERSION)
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'VERSION must be major.minor.patch' >&2; exit 1; }
commit=$(git rev-parse HEAD)
if [[ -n $(git status --porcelain -- VERSION cmd internal Dockerfile.build) ]]; then
  commit=local
fi
if [[ -n "${DOCKYARD_CROSS_CC:-}" ]]; then
  # Explicit fallback for hosts without a Docker daemon. The compiler must
  # target Linux amd64 and provide static libc (for example Zig/musl).
  mkdir -p bin/linux
  for name in dockyard dockyardctl; do
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC="$DOCKYARD_CROSS_CC" go build -trimpath \
      -ldflags="-s -w -linkmode external -extldflags -static -X github.com/ziqx/ziqx-dockyard-service/internal/buildinfo.Version=$version -X github.com/ziqx/ziqx-dockyard-service/internal/buildinfo.Commit=$commit" \
      -o "bin/linux/$name" "./cmd/$name"
  done
else
  docker build --platform linux/amd64 --build-arg "DOCKYARD_VERSION=$version" --build-arg "DOCKYARD_COMMIT=$commit" --file Dockerfile.build --output bin/linux .
fi
install -d desktop/src-tauri/resources/ubuntu
install -m 0755 bin/linux/dockyard bin/linux/dockyardctl desktop/src-tauri/resources/ubuntu/
DOCKYARD_COMMIT="$commit" python3 - <<'PY'
import hashlib
import json
import os
from pathlib import Path

root = Path('desktop/src-tauri/resources/ubuntu')
commit = os.environ['DOCKYARD_COMMIT']
data = {'source_commit': commit, 'version': Path('VERSION').read_text().strip()}
for name in ('dockyard', 'dockyardctl'):
    data[name + '_sha256'] = hashlib.sha256((root / name).read_bytes()).hexdigest()
(root / 'manifest.json').write_text(json.dumps(data, indent=2) + '\n')
PY
