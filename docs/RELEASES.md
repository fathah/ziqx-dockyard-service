# Automated Ubuntu releases

The [release workflow](../.github/workflows/release.yml) runs on pushes to the `release` branch. It checks out that exact commit, installs the current stable Go toolchain, verifies modules, runs race tests and vet, then builds both binaries natively on Ubuntu 22.04 with CGO enabled for SQLite.

The target is **Linux x86-64 (`amd64`) on Ubuntu 22.04 or newer**. These are dynamically linked native binaries; they require the host's compatible glibc. This workflow does not build ARM64 binaries. Build metadata records the source commit, Actions run, Go/compiler and glibc versions.

The release reads the server version from [`VERSION`](../VERSION) and embeds that version and the source commit in both Go binaries. Inspect either binary without root credentials or a running service using `dockyard -version` or `dockyard -version-json` (`dockyardctl` supports the same flags). Development builds without release flags report `dev` and `unknown`.

Each successful commit produces a GitHub Release tagged `release-<full-commit-sha>` with these assets:

- `dockyard-linux-amd64` — privileged deployment daemon
- `dockyardctl-linux-amd64` — backend/operator client
- `dockyard-ubuntu-amd64.tar.gz` — install bundle with both binaries, systemd unit, sample policy, Caddy include and documentation
- `BUILD-INFO.txt` — source and build metadata
- `SHA256SUMS` — checksums for both binaries, build metadata and the install bundle

Upload happens to a draft first; the workflow publishes it only after all assets upload successfully. A failed upload can leave a draft, which a rerun resumes. Rerunning an already published commit preserves its existing assets. Separate commit releases remain available. Action versions are pinned to verified commits; the Go toolchain follows the stable channel, so different source releases can use newer Go patches.

The workflow uses GitHub's built-in token with `contents: write` for release/tag creation. No personal token or repository secret is required. Repository or organization policy must permit the workflow and this permission. Creating a release publishes downloadable builds; it does not deploy to a VPS or establish production acceptance.

After downloading the assets from the desired release's **Releases** page, verify and install on the Ubuntu VPS:

```sh
sha256sum --check SHA256SUMS
sudo install -o root -g root -m 0755 dockyard-linux-amd64 /usr/local/bin/dockyard
sudo install -o root -g root -m 0755 dockyardctl-linux-amd64 /usr/local/bin/dockyardctl
```

Then follow [installation and credentials](INSTALL.md). The daemon requires Linux/root, Docker Compose, Caddy and private policy/credentials before it can start. The downloaded operator client runs as a regular user with its authorized credentials.

## Desktop 0.1.10 — 2026-10-30

- Use a compact, searchable service list with a separate details panel for dependencies, container identities, logs, terminal, domains and individual updates.
- Match the supplied blue accent and neutral surfaces; align project tabs to the left.
- Review image updates from a project banner and open logs with the selected service and instance already chosen.

Bundles the same VPS service **0.7.37** as desktop 0.1.9. This desktop-only update requires no new server deployment.

## 0.7.37 · Desktop 0.1.9 — 2026-10-30

- Update a replicated stateless service by starting the full replacement group, checking every replica and switching all its Caddy upstreams together. Sibling services and databases keep running.
- Update internal API replicas behind an existing HAProxy container using the opt-in [HAProxy integration](HAPROXY_UPDATES.md). Verify discovery and health before draining old backends; retain old replicas while requests are active or proxy state is uncertain.
- Show replica counts and review the replica/proxy plan in the desktop. Older servers that report the single-container restriction now show the server update action.

Requires VPS service **0.7.37+** and desktop **0.1.9+**. Rebuild the Ubuntu bundle before packaging the desktop. Installing the Dockyard server update does not restart application containers; configuring HAProxy's private Runtime API is a separate one-time setup.

## 0.7.36 · Desktop 0.1.8 — 2026-10-23

- Connect domains while a Compose project is stopped. Assignments show **Pending activation** and activate routing after a successful start and health checks.
- Unlink individual service domains while running or stopped. DNS records and containers are preserved, and verified unlinking frees the hostname for another service or project.
- Recover interrupted domain changes by verifying their reviewed Caddy state, including removal of the last domain.

These features require VPS service **0.7.36+** and desktop **0.1.8+**. Rebuild the Ubuntu bundle with `npm run server:bundle` before packaging the desktop app so its updater includes the matching service binaries.
