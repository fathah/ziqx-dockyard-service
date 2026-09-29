# SQLite metadata

SQLite is the authoritative inventory and job journal. The default database is `/var/lib/dockyard/state.db`, controlled by `state_dir` in root policy. There is no separate database server or SQL API. Project, release, job and audit reads already used SQLite; service, slot, assigned-domain and Cloudflare record associations now have durable indexes too.

| Data | Storage |
| --- | --- |
| Project configuration, lifecycle state, release history | `projects` snapshots |
| Unique immutable app/environment-to-project identity | `project_targets` |
| Slot-to-release, Compose and environment bindings | `project_slots` |
| Verified service image, template, health and ownership metadata | `compose_revisions` |
| Currently assigned domains | `project_domains` |
| Retained domain and port reservations | `domains`, `ports` |
| Project/hostname-to-Cloudflare record IDs | `dns_records` |
| Jobs, retry identities and transactional audit | `jobs`, `requests`, `audit` |

Project snapshots, slot/domain indexes, DNS outcomes, job state and audit events commit in the same transaction. Compose metadata is immutable by project/revision and is indexed after policy and Compose validation, before deploy acceptance. An unsuccessful admission can leave an unreferenced private revision; inventory only exposes revisions bound to recorded slots.

## Inventory APIs

All reads below require the signed `deploy.read` scope and access to the project. They read SQLite without scanning `/docker`, invoking Docker/Caddy, or calling Cloudflare:

- `GET /v1/projects` and `GET /v1/projects/{id}`
- `GET /v1/projects/{id}/releases`
- `GET /v1/projects/{id}/services`
- `GET /v1/projects/{id}/domains`

Services return a `services` array with `slot`, `name`, `image`, `template_id`, `compose_revision` and `environment_revision`. Both retained slots may appear, including stopped or candidate stacks; this is recorded inventory, not a report of running containers. Legacy releases omit `compose_revision`.

Domains return a `domains` array with `hostname`, `assigned` and an optional `dns_record_id`. Removed domains stay reserved with `assigned: false`. Assignment reflects recorded project configuration; it does not prove DNS propagation or the live Caddy route. The OpenAPI contract documents both endpoints.

Environment values, submitted command arguments and raw user YAML are excluded from the service index and inventory responses. Private `.env` and immutable Compose files remain deployment artifacts. Lifecycle commands still verify manifest hashes and current root policy before invoking Docker. `/status`, logs, next-port checks and deployment operations continue to inspect the necessary live state; SQLite does not replace those checks or log redaction's retained-secret reads.

## Migration and operations

Startup performs an additive transactional schema migration, tracked with SQLite `user_version` (currently 2). It preserves existing projects, jobs, idempotency records, reservations and audit history, and backfills assigned-domain and slot indexes from database snapshots. The engine imports missing service metadata from verified retained Compose revisions once. Recorded slot manifests are still verified on startup; subsequent startups do not reread every historical manifest already indexed. Schema 2 classifies existing projects as production with `app_id` equal to the existing project ID, preserving their live mode and paths, and migrates queued/recovery job snapshots. New app/environment pairs are unique and immutable. A newer schema version is rejected. See [deployment environments](ENVIRONMENTS.md).

Older project snapshots contain a flat list of Cloudflare IDs without hostname associations. Those IDs remain intact. A successful DNS job or offline DNS reconciliation records the verified hostname association; migration does not guess it.

The database uses WAL, FULL synchronization, foreign keys, one connection and a bounded busy timeout. Its directory is private and the database file is mode `0600`; production ownership follows the root-only daemon. No new policy setting is required.

Back up through a consistent SQLite backup or after stopping the agent and closing the database. Include matching project artifacts, Caddy files, root policy, stable fingerprint key and application-consistent named-volume backups. Copying a live `state.db` alone can omit WAL data. Restore the matching set and reconcile before writes, as described in [installation and recovery](INSTALL.md#backups-and-quotas).
