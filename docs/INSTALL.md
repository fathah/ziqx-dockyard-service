# VPS installation and recovery

This is a staging installation procedure, not a record of an installed agent. Run on a disposable Linux VPS first. The agent controls Docker and is consequently privileged at the host level. Native TLS/HMAC and constrained operations reduce its exposed capabilities; systemd hardening does not make Docker access an unprivileged boundary.

## Prepare the host

You can download prebuilt Ubuntu x86-64 binaries from GitHub Releases after pushing to the `release` branch, verify their checksums, and install them as described in [automated releases](RELEASES.md). The source-build procedure below is an alternative.

Use a patched, supported Linux distribution, Docker Engine, Docker Compose >=2.30, and Caddy. The agent requires a trusted `caddy` service account. Keep the Docker and Caddy admin APIs off public networks. Pin the actual versions used in acceptance testing. The repository has a Go 1.24 language floor; build production releases with a current patched Go toolchain. The provided Linux builder selects Go 1.27.1 ([Go releases](https://go.dev/dl/)). It is a build/export image, not the daemon's runtime image.

```sh
make linux-build
sudo install -o root -g root -m 0755 bin/linux/dockyard /usr/local/bin/dockyard
sudo install -o root -g root -m 0755 bin/linux/dockyardctl /usr/local/bin/dockyardctl

sudo install -d -o root -g root -m 0700 /etc/dockyard /etc/dockyard/tls /etc/dockyard/docker /var/lib/dockyard /docker
sudo install -d -o root -g caddy -m 0750 /etc/caddy/dockyard
sudo install -o root -g root -m 0600 deploy/config.example.json /etc/dockyard/config.json
```

Review the example configuration and replace every placeholder before startup. New Compose projects need no templates. The sample grants `compose.admin` to its server-wide credential, which permits root-equivalent host control through Compose. Keep this credential private. Configure project quotas, reserved ports/control-plane hostnames, and optional Cloudflare settings.

Docker Compose validates submitted stacks and `.env` contents. Ordinary image tags, multiple services, networks, builds, mounts and volumes are supported. Additional files/build contexts must exist on the VPS. Keep databases in the same Compose stack; eligible production apps can use individual Seamless updates with healthchecks. Database and worker updates use a controlled restart. See [Compose deployments](COMPOSE.md). The [legacy sample](../deploy/config.legacy.example.json) is retained for existing template-mode installations; do not remove templates referenced by existing projects.

## Caddy and API network access

Merge [Caddyfile.include.example](../deploy/Caddyfile.include.example) into the existing main Caddyfile: one global `admin unix//run/caddy/admin.sock|0600` option and a top-level `import /etc/caddy/dockyard/*.caddy`. Keep manual sites outside the managed include. Make the main file root-owned and not group/other writable. Test it with the installed Caddy before enabling the agent. Caddy supports permissioned Unix socket administration ([Caddy conventions](https://caddyserver.com/docs/conventions), [admin option](https://caddyserver.com/docs/caddyfile/options#admin)).

If needed, install this Caddy systemd drop-in to create its private runtime directory:

```ini
[Service]
RuntimeDirectory=caddy
RuntimeDirectoryMode=0700
```

The agent checks that the admin socket has mode 0600 and that its ancestors are owned by root or the trusted `caddy` account without group/other write permission. Caddy must be the sole route writer alongside the coordinated agent. Out-of-band config edits cause a disk/live divergence and block route changes.

The root systemd service retains `CAP_DAC_OVERRIDE` so it can traverse Caddy's private service directory and connect to the caddy-owned socket. Its bounding set permits only this capability; `NoNewPrivileges`, the read-only filesystem mounts and configured writable paths still apply. An empty capability bounding set prevents the agent from reaching this private socket even when its Unix user is root.

The agent defaults to `127.0.0.1:9123`; only loopback/private IPs are accepted. Use a private control network or an SSH tunnel from the Super Admin backend. Restrict network ingress to that backend with your host/network firewall. The server certificate SAN must match the IP/name the backend verifies. Keep the agent's end-to-end mTLS connection intact; an ordinary public Caddy reverse proxy would change the peer certificate and must not be added without a separately designed gateway contract.

## Credentials

Provision a server TLS certificate/key and a dedicated control-plane client CA. The CA private key stays off the VPS. The backend/operator client has its own certificate/key and verifies the agent's server certificate. Each configured HMAC key is additionally pinned to that client's certificate DER hash:

```sh
openssl x509 -in client.crt -outform DER | openssl dgst -sha256
```

Put only the lowercase hexadecimal hash in `certificate_sha256`. Store each HMAC secret and the separate stable idempotency fingerprint secret as base64-encoded random bytes (at least 32 bytes). Create them in a private administrative session, without exposing them in shell history or application logs. Files referenced as secrets must be root-owned mode 0600; all ancestors must be root-owned and not writable by other users. Set the TLS private key to 0600 as well.

Registry credentials belong in the root-owned private Docker config directory. Grant read access to the approved GHCR repositories. Credential helpers must be installed in the runner's fixed `/usr/bin:/bin` PATH or use an appropriate root-protected Docker config. The runner does not inherit the agent's environment, HMAC keys, SSH agent, or arbitrary Docker endpoints.

Cloudflare is optional: remove the entire `cloudflare` object when unused. When enabled, provide a DNS edit token restricted to the configured zone(s), its private token file, zone IDs, and this VPS's public origin IP. Clients cannot substitute a zone/IP. Prefer DNS-only records while testing certificates, then validate the selected proxy mode separately.

Use separate keys with narrow project and scope lists for automation, logs, and provisioning. `projects: ["*"]` grants every project and access to audit records when combined with `deploy.read`; grant it only to the administrative backend. Key rotation may keep both old/new entries temporarily. Both require matching certificate pins. Remove a compromised key/pin and restart the agent to revoke its access; replace CA/certificates if their trust is compromised.

## Start and call

For existing `/docker` projects and Caddy sites, run `sudo dockyard -config /etc/dockyard/config.json -sync-existing` with the daemon stopped to populate safe production inventory first. Normal startup refreshes it and repeats every five minutes. Manual sites and Compose files remain unchanged; existing deployments require an explicit migration before lifecycle control. See [existing services and 24/7 operation](EXISTING.md).

```sh
sudo /usr/local/bin/dockyard -config /etc/dockyard/config.json -check
sudo install -o root -g root -m 0644 deploy/dockyard.service /etc/systemd/system/dockyard.service
sudo systemctl daemon-reload
sudo systemctl enable --now dockyard
sudo journalctl -u dockyard
```

`-check` checks policy, ownership, TLS credentials, and HMAC material without starting the service. Startup additionally requires compatible Compose and a protected Caddy admin socket. The process lock prevents two agents or recovery commands from controlling the same state directory. Paths in the supplied unit match the example policy; update its `ReadWritePaths` if your root policy uses other locations.

Example client request from the backend/operator host:

```sh
dockyardctl -url https://127.0.0.1:9123 -server vps-01 \
  -key-id control-01 -key-file /secure/control-01.key \
  -cert /secure/client.crt -key /secure/client.key -ca /secure/server-ca.crt \
  -scopes projects.write -actor operator-01 \
  -method POST -path /v1/projects -body /secure/create-project.json \
  -idempotency create-demo-01 -request-id req-create-demo-01
```

Keep request body files containing environment values private. Reuse the same operation/request IDs and exact body for uncertain retries. The client refuses mutations without explicit retry IDs. Poll `/v1/jobs/{job_id}` using `deploy.read` before submitting dependent operations.

## OpenAPI reference

Open `https://127.0.0.1:9123/docs` through the same private network/tunnel, using a browser configured to trust the server CA and present a trusted operator client certificate. The viewer is read-only; it does not accept HMAC secrets or execute operations. It contains no CDN assets. Documentation and health access verify the client CA at the listener; signed API calls additionally verify the configured certificate pin and key permissions.

To download the OpenAPI contract without a browser:

```sh
curl --cacert /secure/server-ca.crt \
  --cert /secure/client.crt --key /secure/client.key \
  https://127.0.0.1:9123/openapi.json --output dockyard-openapi.json
```

All documentation assets are embedded in the Go binary. Rebuild and install the agent to publish documentation changes alongside API changes.

## Deployment environment selection

New project creation requires `app_id` plus `environment`; new Compose deploy requests require the same environment as the target project. Grant the operator key each environment's project ID explicitly. The CLI prompts if the body omits the choice, or accepts `-environment development|staging|production` for unattended calls. Reuse that flag/value, unchanged body file and the same request/idempotency IDs for retries. See [environment isolation and upgrades](ENVIRONMENTS.md).

## Interrupted jobs

Any job left running at restart becomes `recovery_required`. Unknown route/DNS outcomes also use that state, and all mutation admissions/worker claims stop. Read APIs remain available. Both app containers are retained; stopping the agent never stops serving apps.

Inspect the job/status, then stop the daemon and reconcile locally:

```sh
sudo systemctl stop dockyard
sudo /usr/local/bin/dockyard -config /etc/dockyard/config.json -reconcile-job job-ACTUAL_ID
sudo systemctl start dockyard
```

Reconciliation requires the full live Caddy JSON to match the adapted disk config, an exact generated route snippet, and a healthy container with the recorded image/environment identity for any serving slot. DNS recovery recognizes or creates only the exact locally authorized ownership-marked record. It never guesses from health alone, reloads Caddy, or stops containers. The interrupted request remains failed with `JOB_INTERRUPTED_RECONCILED`; project state reflects the observed route. A fresh drain is required before the next deployment reuses a retained slot.

If the filesystem/live route is divergent, no recognized snippet exists, or the serving container cannot be verified, reconciliation refuses to clear the block. Preserve evidence and restore a known, coherent configuration under an explicit operator recovery procedure. Automatic compensation of these cases is not implemented. A failed initial provisioning job is left visible rather than silently adopted or retried.

## Backups and quotas

Metadata lives in `state_dir/state.db` (default `/var/lib/dockyard/state.db`). Startup automatically migrates the original SQLite schema and imports missing verified service metadata. Back up the matching state/artifacts before upgrading; do not downgrade a migrated database to an older binary. See [database storage and migration](DATABASE.md).

Back up the SQLite database through a consistent SQLite backup or while the stopped agent has closed it; include project files, immutable Compose/environment revisions, managed Caddy snippets, root policy, and the stable fingerprint key. Back up managed Docker named volumes using an application-consistent database/data backup procedure as well. Treat the entire backup as sensitive. Restore all of these as a matching set and reconcile before accepting writes. Do not copy just `state.db` while ignoring an active WAL.

The first implementation does not automatically remove history, images, environment revisions, or retired port/domain reservations. It caps stored jobs at 10,000, top-level environment entries at 500 (companion directories also count), Compose revisions at 500, and activations at 500 per project, then rejects new work. Monitor disk space and plan a tested archival/retention mechanism before production. Files created before an acceptance transaction fails can become protected orphans; never remove a referenced revision. No broad Docker prune, volume deletion, or Compose down is used.
