# Existing projects and Caddy inventory

Dockyard runs as a native root daemon under systemd. Both binaries install into `/usr/local/bin`; only `dockyard` runs continuously. The supplied unit starts at boot, restarts the daemon after normal/failed exits with a five-second delay, and disables the automatic retry limit. An explicit `systemctl stop dockyard` still stops it. Docker/Caddy are wanted dependencies rather than dependencies whose shutdown permanently stops the control daemon. Startup checks can fail while those dependencies are unavailable; systemd keeps retrying. Serving containers belong to Docker and keep running when Dockyard is stopped.

## Install and run

Extract the `dockyard-ubuntu-amd64.tar.gz` release bundle. It contains binaries named `dockyard`/`dockyardctl`, the systemd unit, sample policy, Caddy include example and documentation. Or use the separate binaries and repository deployment files. Verify the release checksums first.

```sh
sudo install -o root -g root -m 0755 dockyard /usr/local/bin/dockyard
sudo install -o root -g root -m 0755 dockyardctl /usr/local/bin/dockyardctl
sudo install -o root -g root -m 0644 deploy/dockyard.service /etc/systemd/system/dockyard.service
sudo install -d -m 0700 /etc/dockyard /etc/dockyard/tls /etc/dockyard/docker /var/lib/dockyard
sudo install -d -o root -g caddy -m 0750 /etc/caddy/dockyard
```

Create `/etc/dockyard/config.json` from the sample on a first installation, replace placeholders, and provision the root-owned private TLS/HMAC/fingerprint credentials as described in [INSTALL.md](INSTALL.md). Do not replace an existing policy with the example. Keep `/docker` and your current Caddy sites intact. Add the managed import and private Caddy admin socket to the existing Caddyfile as described in the installation guide; do not overwrite its manual sites. Sync alone can run before the admin socket is configured because it only uses `caddy adapt`.

After policy/credentials are configured, run an initial sync while the daemon is stopped:

```sh
sudo dockyard -config /etc/dockyard/config.json -check
sudo dockyard -config /etc/dockyard/config.json -sync-existing
sudo systemctl daemon-reload
sudo systemctl enable --now dockyard
sudo systemctl status dockyard
sudo journalctl -u dockyard -f
```

For upgrades, stop the daemon, back up matching SQLite/project/Caddy state, replace the binaries and unit, then start it again. Do not overwrite the existing policy, `.env` files or application Compose files. This code has not yet been installed or accepted on your actual VPS.

## What sync records

Sync runs once at daemon startup and then every five minutes. The offline `-sync-existing` command uses the same process lock as the daemon. Reading `/v1/inventory` never rescans files or invokes adapters.

- Immediate project directories under `projects_root`, recognizing `compose.yaml`, `compose.yml`, `docker-compose.yaml`, `docker-compose.yml` and their common override filenames.
- Safe service names, literal image references and explicitly published numeric ports. Legacy projects are classified **production**; their current deployment mode is preserved, and blue-green is not enabled by discovery.
- The effective Caddyfile host matchers and static host/port upstreams, obtained through `caddy adapt`, including imported configuration. No reload occurs. The Caddyfile remains authoritative for manual configuration.
- Published ports reported by `docker ps --all`, including Docker projects outside the recognized directories. Docker commands are read-only; Compose files are never passed to `docker compose config` during discovery.
- Associations between Caddy upstreams and projects only when a loopback published port maps unambiguously to one project. Remote/dynamic/socket upstreams or Docker service-name targets can remain unassociated; associations describe topology, not ownership.

The SQLite schema is version 3. `inventory` stores safe observations and timestamps; `inventory_ports` conservatively retains every observed published port. New project allocation checks these reservations as well as managed reservations and live listeners. Failed sources preserve their last successful observations. Missing projects remain with `present: false`; retired observed ports remain reserved until a future explicit retirement procedure. Observe warnings before treating a scan as complete.

Discovery reads bounded trusted Compose files in memory and never reads `.env` files or persists raw YAML, environment values, commands, labels, Caddy credential/header values or full adapted Caddy JSON. Unsafe/symlinked files, interpolation, aliases, ambiguous YAML, port ranges, dynamic values and unmerged override files can leave metadata incomplete. Each project exposes warning codes. No root template, production environment variable or resource policy is guessed from legacy files. Known managed projects use their existing database service index.

## API and management boundary

`GET /v1/inventory` requires signed `deploy.read` plus all-project access (`projects: ["*"]`) because it contains host-wide projects and Caddy sites. It returns `projects`, `sites`, retained `reserved_ports`, last scan/source timestamps and fixed warning codes. Ordinary per-project keys cannot read it. The OpenAPI contract includes every response field.

Legacy IDs use `existing-<directory-name-hash>` and `managed: false`. Existing deployments are inventoried in SQLite and visible through this endpoint; they are not inserted into the controlled project table or silently adopted for stop/restart/deploy/route editing. Controlled projects remain in `/v1/projects`. Taking over a legacy deployment still requires an explicit migration of its approved image/template, port/domain ownership, immutable environment/Compose revisions, health contract and any persistent volumes. Sync alone never changes live services to satisfy the agent's stricter policy.

The sample policy now permits 300 managed project instances, enough identities for 100 apps with development/staging/production each. This is an admission limit, not a guarantee of CPU/RAM/disk capacity. The current deploy worker is host-wide and serial; running applications are independent Docker workloads. Reserve memory/CPU for production slot overlap, monitor disk space, and plan retention for the existing bounded job/revision history. A 100-directory metadata discovery test passes locally; live 50–100-service load and recovery acceptance remains required.
