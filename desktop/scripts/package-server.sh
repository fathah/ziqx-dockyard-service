#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
version=$(cat VERSION)
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'VERSION must be major.minor.patch' >&2; exit 1; }
commit=$(git rev-parse HEAD)
if [[ -n $(git status --porcelain -- VERSION cmd internal Dockerfile.build) ]]; then
  commit=local
fi
docker build --platform linux/amd64 --build-arg "DOCKYARD_VERSION=$version" --build-arg "DOCKYARD_COMMIT=$commit" --file Dockerfile.build --output bin/linux .
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
