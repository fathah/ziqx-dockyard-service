# Implementation status — 2026-09-29

The project has moved from documents only to an initial Go service implementation. This is a staging candidate, not an approved production release.

Implemented:

- Read-only startup/five-minute existing Compose/Caddy inventory, offline `-sync-existing`, production classification for legacy projects, SQLite schema-3 snapshots/observed-port reservations, source-failure preservation and host-wide authenticated `/v1/inventory`. Discovery preserves files/containers and excludes secrets; automatic legacy takeover remains pending. Systemd keeps retrying after exits; the sample capacity is 300 managed instances.
- Development/staging/production selection in create/deploy requests and the operator CLI, separate projects per app/environment with a unique immutable SQLite target index, production-only blue-green, single-instance development/staging, and legacy project/job migration preserving deployment mode.
- Linux/root-only daemon, process locking, native TLS 1.3 mTLS, certificate-to-HMAC-key pins, canonical signed requests, freshness checks, per-key scopes/project policy, bounded admission/rate limits.
- Strict root policy, duplicate/unknown JSON rejection, input/path/domain/image/port/environment constraints, pinned project templates, private secret revisions, fixed command runner with output/deadline/process-group bounds.
- SQLite WAL/FULL durable jobs, independent keyed retry fingerprints, request-ID uniqueness, atomic project/domain/port reservations, FIFO worker, transactional phase/outcome audit.
- Versioned SQLite migration, immutable service-revision metadata, transactional slot/assigned-domain indexes and hostname-to-Cloudflare IDs; database-only project/release/service/domain inventory reads. Existing history and reservations are preserved. Compose integrity checks remain mandatory for privileged operations.
- User-supplied Compose YAML for every new deployment, strict in-memory validation before Compose config validation, up to eight root-approved services, immutable Compose/environment revisions, all-service health/ownership/image/environment/mount/port checks, per-service logs, blue-green stateless stacks, single-slot managed named volumes, rollback/restart/confirmed stop/start. Operator `compose.yml`/`.env` mirrors update after activation.
- Generated Caddy include files, manual/control-domain protections, full disk/live config comparison, classified reload results, container preservation on unknown outcomes, offline root reconciliation.
- Container log snapshots with caps/control stripping/known-value redaction, project/status/release/job reads, audit pagination, next-port lookup, Cloudflare zone-constrained ownership-marked subdomain creation.
- Backend/operator client, sample root policy, systemd hardening unit, Caddy include example, native Linux binary builder, API/install/staging documentation.
- OpenAPI 3.1.1 JSON contract for all implemented API and health/documentation endpoints, with embedded read-only `/docs` viewer and `/openapi.json` download. Documents mTLS/HMAC, custom scopes, exact signing/retry behavior, schemas, and examples.

Local verification: the service tests (including table/fault cases, SQLite migration/restart, transactional rollback, inventory without project files and cached-metadata integrity checks, environment isolation/mode enforcement, CLI selection/retry stability and legacy target migration, 100-project existing inventory, failed-source preservation and secret exclusion) passed under `DOCKYARD_COMPOSE_TEST=1 go test -race ./...`; the real Compose parser checks for literal values, multi-service normalization, and stable persistent volume names, `go vet ./...`, and native development builds of both binaries passed. The OpenAPI contract passes the official OpenAPI 3.1 JSON Schema and example validation; automated checks cover local references, authentication requirements, response model fields, and static documentation isolation. Development host: Go 1.24.5 on macOS arm64, Docker Compose 5.5.0. The linker emits a macOS CGO/race `LC_DYSYMTAB` warning, while checks complete successfully. Privileged adapters use injectable fault tests; no live Docker daemon, Caddy process, Cloudflare account, Linux build, or VPS installation has been validated here.

Differences from the broader September 17 specification:

- The API is project-first (`POST /v1/projects`, `PUT /v1/projects/{id}/routes`) rather than hostname-upsert-first. DNS creation is a separate job for an assigned domain.
- Native end-to-end mTLS is implemented instead of relying on certificate headers from a Caddy gateway. The API binds loopback/private IPs only.
- Scheduling is a single host-wide worker with one outstanding job per project, rather than concurrent project workers with per-project queues.
- Environment creation is part of deploy admission; separate secret-revision staging/metadata APIs, multiline dotenv encoding, automatic retention/archival, JSONL audit outbox export, metrics, TLS-readiness tracking, live port migration, template/mode migration, and broader automatic recovery remain pending.
- Removed domain/port reservations are conservatively retained. Quotas stop further work before unlimited history accumulation.

Still required: the real Linux/mTLS/Docker/Caddy traffic/crash tests in [STAGING.md](STAGING.md), independent security review, operational backup/recovery exercises, and Super Admin integration. Existing specifications should not be read as a claim that every acceptance criterion has been met.

## macOS desktop client

A Tauri 2 / Rust macOS desktop client now exists in `desktop/`, with a bundled control-room UI covering managed projects/environments, Compose deployments, blue-green slots, services/releases/logs, routes/DNS, tracked jobs/audit, and production inventory. It requires native Touch ID for enrollment/unlock (without password or Apple Watch fallback), and uses non-synchronized Keychain storage, pinned TLS 1.3/mTLS and signed requests, explicit local command permissions, native write review and durable retry payloads. Local build/security-transport checks are independent of VPS acceptance. Developer ID notarization and real native-auth/Keychain/VPS enrollment acceptance remain required before production use. See [desktop setup](../desktop/README.md).
