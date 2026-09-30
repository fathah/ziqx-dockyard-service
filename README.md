# Ziqx Dockyard Service

A native Go API agent on a Linux VPS, managed by systemd. It creates approved application projects under `/docker/<project>`, controls their Docker Compose slots, edits agent-owned Caddy snippets under `/etc/caddy/dockyard`, and optionally creates Cloudflare subdomains pointing at the configured VPS IP.

**Status: initial implementation, ready for development/staging evaluation. Production security and zero-downtime acceptance are not yet established.** See [current implementation status](docs/STATUS.md), [API contract](docs/API.md), and [VPS installation](docs/INSTALL.md). The September 17 specifications remain the broader design target; the API document describes the code that exists today.

The [OpenAPI 3.1.1 contract](docs/openapi.json) documents every implemented endpoint, request/response schema, signed header, permission, and retry behavior. An installed agent serves a searchable, read-only reference at `/docs` and the contract at `/openapi.json`. Both require a trusted client certificate; the viewer bundles its assets and needs no HMAC signing keys or external services.

The agent requires TLS 1.3 with a verified, pinned client certificate and fresh HMAC signatures. Root policy constrains each key's scopes/projects, image repositories, domain suffixes, port pool, application environment keys, templates, and resource limits. HMAC signing keys stay on the backend or operator client. The Super Admin backend signs calls after checking the operator's permissions.

Users choose development, staging or production before submitting Compose YAML. The same app can have a separate deployment in each environment. Development/staging use one instance; only production permits blue-green. See [deployment environments](docs/ENVIRONMENTS.md). Users submit Compose YAML in each deploy request. The service validates a strict allow-list, injects root-approved runtime limits, and deploys immutable Compose revisions through Docker Compose. Up to eight services are supported, with `app` as the Caddy target. Every service passes health and identity checks before traffic switches. Stateless stacks support blue-green slots; stacks with approved named volumes use single-slot mode. See the [Compose policy and examples](docs/COMPOSE.md).

Mutations return a durable `202` job envelope and support retry deduplication. The first version uses one global worker, one outstanding job per project, and a process lock. Every job transition has a transactional SQLite audit event. Interrupted or ambiguous changes block mutations until a local root recovery command can prove the live route and container identity.

SQLite tracks projects, releases, slots, service revisions, domains, DNS record IDs, port reservations and jobs. Existing Compose directories and effective Caddyfile sites sync into separate safe SQLite inventory at startup and every five minutes; imported deployments are production inventory without automatic takeover. See [existing services and 24/7 operation](docs/EXISTING.md). Inventory APIs read the database without filesystem scans or external calls; Compose and secret files remain verified deployment artifacts. See the [database and migration guide](docs/DATABASE.md).

## Development

Push to the `release` branch to run tests and build Ubuntu x86-64 binaries automatically. GitHub Releases receives `dockyard`, `dockyardctl`, build metadata and checksums for each successful source commit. See [automated releases](docs/RELEASES.md).

The server version lives in [`VERSION`](VERSION). Release and local `make build` binaries print it with `dockyard -version` or `dockyard -version-json`; the JSON output also reports the build commit and target platform. Local builds identify their commit as `local`.

Go 1.24+ and a C compiler are needed for the pinned SQLite driver (`github.com/mattn/go-sqlite3`); use a supported, patched Go release for production builds. The Linux builder selects Go 1.27.1. Native host binaries use libc; build on a compatible distribution/architecture. A macOS build is useful for development, but the daemon refuses to run outside Linux or without root.

```sh
make test
make check
make compose-check  # Installed Docker CLI/Compose; no daemon needed
make build
make linux-build   # Docker builder; exports Linux binaries into bin/linux
```

`make test` exercises authentication, permissions, strict input handling, reservations, persistence, command containment, logs, recovery boundaries, and deployment failures with injectable adapters. `make compose-check` verifies literal environment/command values, multi-service normalization, port confinement, and stable volume names through the actual Compose parser. These checks supplement the [real Linux acceptance procedure](docs/STAGING.md).

## macOS desktop control room

[Dockyard Desktop](desktop/README.md) is the Tauri 2 / Rust macOS client for this agent. It includes environment-aware Compose deployment, blue-green slots, services/logs/releases, domain and DNS controls, jobs/audit, and existing VPS inventory. It keeps API credentials in macOS Keychain and signs requests in Rust. Build/run/enrollment instructions and the limits of desktop-exclusive credentials are documented there.

For a new server, the desktop offers **Set up Dockyard on your server**: choose Ubuntu, enter its IP/SSH port, verify its host fingerprint, and supply the root password in a native secure prompt. The installer uses bundled Ubuntu binaries, creates the systemd service and restricted SSH connector, generates enrollment automatically, and saves the server IP for future connections. See [first-time setup and recovery](desktop/FIRST_TIME.md).
