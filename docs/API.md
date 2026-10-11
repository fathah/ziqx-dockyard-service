# Implemented API contract

The machine-readable [OpenAPI 3.1.1 contract](openapi.json) includes request examples, response schemas, error responses, authentication, and per-operation permissions. The agent embeds this file and a searchable, read-only viewer: `GET /openapi.json`, `GET /docs`, and its fixed `/docs/style.css` and `/docs/app.js` assets. These resources require TLS client authentication but no signed headers. They use only local assets and cannot execute API operations or store signing credentials. Client certificate pins apply to signed `/v1/` calls; static docs and health resources use the listener's client CA verification.

All `/v1/` operations require mTLS and the [existing signing contract](IMPLEMENTATION_SPEC.md#5-signed-request-protocol). The implementation additionally pins each signing key to a client-certificate DER SHA-256 fingerprint and a locally configured scope/project allow-list. Sign the exact transmitted body bytes and exact path/query. Identifiers are bounded ASCII; encoded paths, duplicate security headers, duplicate JSON keys, unknown JSON fields, duplicate/unknown query keys, and noncanonical scopes are rejected. Maximum body size is 128 KiB. Timestamp skew is at most 60 seconds.

The agent defaults to `https://127.0.0.1:9123`; it can bind an explicitly configured private IP. It rejects wildcard listening addresses. Access it from the backend through a private network or tunnel. `/healthz` and `/readyz` still require mTLS at the TLS listener; they do not require HMAC. Readiness currently represents local state/admission, while project `/status` checks the route and container dependencies.

| Method | Path | Required scope | Behavior |
| --- | --- | --- | --- |
| GET | `/v1/projects` | `deploy.read` | Projects allowed for this key |
| GET | `/v1/inventory` | `deploy.read` plus all-project access | Cached host-wide existing Compose/Caddy metadata, timestamps, warnings and retained observed ports |
| GET | `/v1/inventory/{id}/migration` | `deploy.read` plus all-project access | Read-only safe-first migration checks for one observed project; no takeover or secrets |
| GET | `/v1/projects/{id}` | `deploy.read` | Safe metadata; no environment values |
| GET | `/v1/projects/{id}/status` | `deploy.read` | Route coherence, active container health, busy state, expected downtime |
| GET | `/v1/projects/{id}/releases` | `deploy.read` | Successfully activated image/environment pairs |
| GET | `/v1/projects/{id}/services` | `deploy.read` | Recorded service inventory; native active services also include update eligibility and dependency names from the immutable Compose manifest |
| GET | `/v1/projects/{id}/domains` | `deploy.read` | SQLite inventory of assigned/reserved domains and known Cloudflare record IDs |
| GET | `/v1/projects/{id}/logs` | `deploy.logs` | Bounded, redacted snapshot |
| GET | `/v1/jobs/{job_id}` | `deploy.read` | Job outcome/phases/warnings; key must allow its project |
| GET | `/v1/ports/next` | `projects.write` | Next free allowed port; advisory, not a reservation |
| GET | `/v1/audit?after=0` | `deploy.read` plus all-project access | Next 100 transactional audit events |
| POST | `/v1/projects` | `projects.write` | Create project files, reserve ports/domain, install maintenance route |
| PUT | `/v1/projects/{id}/routes` | `sites.write` | Replace assigned domain list; update ports while stopped |
| POST | `/v1/projects/{id}/dns` | `dns.write` | Create a Cloudflare record for an already assigned subdomain |
| POST | `/v1/projects/{id}/deploy` | `deploy.execute`; also `deploy.environment` when new environments supplied | Validate Compose YAML; full-stack deployment or `mode: seamless` configuration rollout (also requires `projects.write sites.write`) |
| POST | `/v1/projects/{id}/service-update-preview` | `deploy.execute`; Seamless also needs `projects.write sites.write` | Review a selected service, immutable configuration, routes and network identities; no live mutation |
| POST | `/v1/projects/{id}/image-status` | `deploy.read` | Start/poll a background GHCR metadata check; no image layers or containers changed |
| POST | `/v1/projects/{id}/image-prepare` | `deploy.execute` and server-wide `compose.admin` | Start/poll a selected-service image download; containers and routes stay running |
| POST | `/v1/projects/{id}/service-update` | Same as preview; server-wide `compose.admin` required | Pull and update only the reviewed service; skip unchanged image; preserve dependencies |
| POST | `/v1/projects/{id}/rollback` | `deploy.rollback` | Activate a retained Compose/environment configuration |
| POST | `/v1/projects/{id}/restart` | `deploy.execute` | Rolling in blue-green mode; downtime in single-slot mode |
| POST | `/v1/projects/{id}/stop` | `deploy.stop` | Confirmed maintenance route, drain, then stop identified services |
| POST | `/v1/projects/{id}/start` | `deploy.lifecycle` | Restore the recorded release from stopped state |

Mutations require `Content-Type: application/json`, stable `Idempotency-Key` and `X-Request-ID`, and have no query parameters. Retries must preserve actor, request ID, method, path, canonical scopes, and raw body, while refreshing timestamp/signature. The transport key may rotate. The identical stored acceptance envelope is returned even when the original job has completed; poll its job ID for the current outcome. Different content returns `409 IDEMPOTENCY_CONFLICT`; reusing a request ID under a different idempotency key returns `409 REQUEST_ID_CONFLICT`. The first version retains identity records until a planned archival mechanism is implemented.

```json
{"job_id":"job_illustration","project_id":"demo","action":"deploy","status":"queued"}
```

## Create and deploy

New projects omit `template_id` and require a server-wide credential explicitly granted `compose.admin`, in addition to normal signed action scopes. This grants root-equivalent Docker control. Domains are optional; single-instance is the default in every environment.

```json
{"id":"demo","app_id":"demo","environment":"production","domains":[]}
```

After the create job succeeds:

```json
{"environment":"production","compose_yaml":"services:\n  web:\n    image: nginx:${TAG}\n    ports: [\"8080:80\"]\n","env_file":"TAG=alpine\n"}
```

Docker Compose validates the YAML and `.env` inputs before admission. Each is limited to 64 KiB; signed JSON bodies must fit the 128 KiB request limit. Referenced files and build contexts must exist on the VPS. `env_file` contains raw dotenv contents; omission reuses the current snapshot. Compose-mode deployments always require `deploy.environment`. No template, mandatory `app` service, registry allowlist or image digest is required.

Optional `domains`, `route_service` and `route_port` on creation enable managed Caddy routing. Only compatible stateless production stacks support `zerodowntime: true`. See [Compose deployments](COMPOSE.md) for lifecycle, blue-green, persistent-data and rollback behavior. Older projects explicitly created with `template_id` retain the [legacy policy](LEGACY_COMPOSE.md).

## Routes and DNS

Project, release, service and domain inventory reads use SQLite without accessing project files or live adapters. Service entries contain `slot`, `name`, `image`, `template_id`, `compose_revision` (omitted for legacy releases) and `environment_revision`. Domain entries contain `hostname`, `assigned` and optional `dns_record_id`; retired reservations remain visible with `assigned: false`. These are recorded metadata, not live health, DNS propagation or Caddy verification. See [database storage and migration](DATABASE.md).

`PUT /v1/projects/demo/routes` replaces the domain list:

```json
{"domains":["new.example.com"],"port":3005,"secondary_port":3006}
```

Port changes require the project to be stopped; omitted ports stay unchanged. Domain-only changes can apply to running projects. Caddy manual sites/control-plane domains are protected. Removed domains/ports stay reserved in this first version. Mode/template migration and automatic retirement are deferred.

`POST /v1/projects/demo/dns`:

```json
{"hostname":"new.example.com"}
```

The hostname must already belong to the project's domain list and be a subdomain of a configured Cloudflare zone. Record type is A/AAAA according to the locally configured origin IP. Callers cannot choose record content, zone IDs, proxy mode, or TTL. An exact record with this server/project ownership marker is an idempotent no-op; unrelated records are rejected. DNS creation and route updates are separate jobs. No cross-service atomicity, automatic DNS deletion, propagation, or public TLS readiness is claimed. Cloudflare uses a zone-restricted DNS API token ([Cloudflare API](https://developers.cloudflare.com/api/resources/dns/subresources/records/methods/create/)).

## Lifecycle, logs, errors

Rollback accepts `{"release_id":"rel_illustration"}` or `{}` for the preceding activation. Restart/start accept `{}`. Stop requires `{"confirmation":"demo"}`. Deploy rejects a stopped project; start restores its recorded release. Single-slot failures after maintenance leave traffic in maintenance until an explicit start succeeds.

Logs accept `service` (default `app`, must belong to the selected slot revision), `slot=active|inactive|blue|green`, `tail=1..2000` (default 200), and `since` as a positive Go duration up to 24h (default 30m). Response: `{"slot":"blue","service":"app","logs":"...","truncated":false}`. Output is capped at 2 MiB, stripped of unsafe controls, and redacted against retained app and companion environment values. Application-transformed/unrelated secrets cannot be reliably recognized; applications must avoid secret logging.

Log reads have a separate concurrency cap of two. Redaction refuses more than 5000 distinct retained values or 1 MiB of unique value data with `LOG_REDACTION_LIMIT`, rather than exposing output or allowing unbounded redaction work.

One outstanding job per project returns `409 PROJECT_BUSY` until completion; unrelated projects enter a global FIFO with one worker. Queue/project/history quotas reject further work when exhausted. Uncertain side effects use `recovery_required` and globally block mutations. See the local recovery procedure in [INSTALL.md](INSTALL.md).

Errors follow `{"error":{"code":"PORT_IN_USE","message":"The operation could not be completed.","request_id":"..."}}`. Auth failures are 401; scope/project failures 403; invalid inputs 400/422; missing resources 404; conflicts 409; quotas 429; unavailable dependencies/recovery 503. A job accepted with 202 reports any later failure on its job resource.

## Individual service update request

Preview and execute use `expected_release_id` (the current full Compose slot release), `service` and `mode` (`restart` or `seamless`). Seamless requires `container_port` and accepts `readiness_path`; restart omits both. Execute additionally sends the exact preview's `review_sha256`. No source files or environment values are transmitted. The review is bound to the project snapshot, previous image, actual Docker networks, routes and available ports. Changed reviews return `409 SERVICE_REVIEW_CHANGED`. Accepted jobs use action `service_update` and retain the full recovery plan privately; retries return the same job. GET services adds `updatable`, `seamless`, `container_ports` and `update_reason`, and shows the active service image/manifest from the recorded override.

From 0.7.22, seamless preview returns only the selected service's assigned domains, including explicit `service_routes`. Its `container_port` must match those routes (`409 SERVICE_ROUTE_MISMATCH` otherwise); routing several container ports requires controlled restart. The candidate records its own container port and readiness path, and Caddy generation resolves each domain through its selected service's current instance. Sibling instances and routes survive cutover, rollback and reconciliation. Free reserved service ports are reused; the pool grows if all entries are occupied by active replacements.

## Seamless configuration deployments

From VPS service 0.7.24, `POST /v1/projects/{id}/deploy` accepts `mode: "seamless"` with the submitted `compose_yaml`, `env_file`, and a nonempty `expected_release_id`. Requires `deploy.environment deploy.execute projects.write sites.write` and server-wide Compose authority. Running production single-stack projects with managed domains can replace changed eligible website services while keeping databases and unchanged services running. The resolved configuration is compared with the current full release; additions/removals, shared resource edits, network/mount changes, worker/database changes, local builds, and incompatible app settings are rejected before Docker mutations. Domain or route-target changes require a controlled deployment. An unchanged configuration can still pull the routed apps' current image tags.

Accepted jobs use `configuration_update`. Environment-only changes create replacements even when image IDs match. All replacements pass health checks before a single route switch; old apps drain before retirement. Failed readiness leaves the previous source/environment release active. Post-switch failure restores the old routes when the old instances can be verified. Interrupted jobs reconcile verified old/new route snapshots without guessing. Successful activation records the submitted full source/environment as current and clears only the deployed draft. `mode: "restart"` (or omitted mode) retains full-stack deployment behavior. There is no automatic fallback from seamless to restart.

From 0.7.25, `image-status` accepts `{ "expected_release_id": "rel-current" }`; `image-prepare` additionally requires `service` and accepts optional `retry: true` to retry a failed pull. Both are signed POST requests that return a task snapshot: `202` on start, `200` on subsequent polls. States are `checking`/`pulling`, `ready`, or `failed` with a safe error code. Poll at about 1.5 seconds while active. These bounded, deduplicated tasks change only the image cache, do not reserve the project, and do not return a durable deployment job. Checks are cached for one minute and pulls for ten minutes per exact project snapshot. Registry checks report per-service `available`, `current`, `pinned`, or `unknown`, with configured image references and config digests; failures never imply an image is current. `service-update-preview` returns `prepared_image` when a completed pull is available and binds that digest into its review hash. Execution journals that digest and inspects it locally without pulling the mutable tag again. Expiry or snapshot changes invalidate the review; older clients that did not prepare an image retain the normal pull-before-start behavior.
