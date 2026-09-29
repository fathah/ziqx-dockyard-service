# Implemented API contract

The machine-readable [OpenAPI 3.1.1 contract](openapi.json) includes request examples, response schemas, error responses, authentication, and per-operation permissions. The agent embeds this file and a searchable, read-only viewer: `GET /openapi.json`, `GET /docs`, and its fixed `/docs/style.css` and `/docs/app.js` assets. These resources require TLS client authentication but no signed headers. They use only local assets and cannot execute API operations or store signing credentials. Client certificate pins apply to signed `/v1/` calls; static docs and health resources use the listener's client CA verification.

All `/v1/` operations require mTLS and the [existing signing contract](IMPLEMENTATION_SPEC.md#5-signed-request-protocol). The implementation additionally pins each signing key to a client-certificate DER SHA-256 fingerprint and a locally configured scope/project allow-list. Sign the exact transmitted body bytes and exact path/query. Identifiers are bounded ASCII; encoded paths, duplicate security headers, duplicate JSON keys, unknown JSON fields, duplicate/unknown query keys, and noncanonical scopes are rejected. Maximum body size is 128 KiB. Timestamp skew is at most 60 seconds.

The agent defaults to `https://127.0.0.1:9123`; it can bind an explicitly configured private IP. It rejects wildcard listening addresses. Access it from the backend through a private network or tunnel. `/healthz` and `/readyz` still require mTLS at the TLS listener; they do not require HMAC. Readiness currently represents local state/admission, while project `/status` checks the route and container dependencies.

| Method | Path | Required scope | Behavior |
| --- | --- | --- | --- |
| GET | `/v1/projects` | `deploy.read` | Projects allowed for this key |
| GET | `/v1/inventory` | `deploy.read` plus all-project access | Cached host-wide existing Compose/Caddy metadata, timestamps, warnings and retained observed ports |
| GET | `/v1/projects/{id}` | `deploy.read` | Safe metadata; no environment values |
| GET | `/v1/projects/{id}/status` | `deploy.read` | Route coherence, active container health, busy state, expected downtime |
| GET | `/v1/projects/{id}/releases` | `deploy.read` | Successfully activated image/environment pairs |
| GET | `/v1/projects/{id}/services` | `deploy.read` | SQLite inventory of services and revisions in recorded slots |
| GET | `/v1/projects/{id}/domains` | `deploy.read` | SQLite inventory of assigned/reserved domains and known Cloudflare record IDs |
| GET | `/v1/projects/{id}/logs` | `deploy.logs` | Bounded, redacted snapshot |
| GET | `/v1/jobs/{job_id}` | `deploy.read` | Job outcome/phases/warnings; key must allow its project |
| GET | `/v1/ports/next` | `projects.write` | Next free allowed port; advisory, not a reservation |
| GET | `/v1/audit?after=0` | `deploy.read` plus all-project access | Next 100 transactional audit events |
| POST | `/v1/projects` | `projects.write` | Create approved project files, reserve ports/domain, install maintenance route |
| PUT | `/v1/projects/{id}/routes` | `sites.write` | Replace assigned domain list; update ports while stopped |
| POST | `/v1/projects/{id}/dns` | `dns.write` | Create a Cloudflare record for an already assigned subdomain |
| POST | `/v1/projects/{id}/deploy` | `deploy.execute`; also `deploy.environment` when new environments supplied | Validate Compose YAML, pull/start all services, health-gate stack, switch route, drain previous slot |
| POST | `/v1/projects/{id}/rollback` | `deploy.rollback` | Activate a retained exact image/environment pair |
| POST | `/v1/projects/{id}/restart` | `deploy.execute` | Rolling in blue-green mode; downtime in single-slot mode |
| POST | `/v1/projects/{id}/stop` | `deploy.stop` | Confirmed maintenance route, drain, then stop identified services |
| POST | `/v1/projects/{id}/start` | `deploy.lifecycle` | Restore the recorded release from stopped state |

Mutations require `Content-Type: application/json`, stable `Idempotency-Key` and `X-Request-ID`, and have no query parameters. Retries must preserve actor, request ID, method, path, canonical scopes, and raw body, while refreshing timestamp/signature. The transport key may rotate. The identical stored acceptance envelope is returned even when the original job has completed; poll its job ID for the current outcome. Different content returns `409 IDEMPOTENCY_CONFLICT`; reusing a request ID under a different idempotency key returns `409 REQUEST_ID_CONFLICT`. The first version retains identity records until a planned archival mechanism is implemented.

```json
{"job_id":"job_illustration","project_id":"demo","action":"deploy","status":"queued"}
```

## Create and deploy

```json
{
  "id": "demo",
  "app_id": "demo",
  "environment": "production",
  "template_id": "web-node",
  "domains": ["demo.example.com"],
  "zerodowntime": true,
  "port": 3001,
  "secondary_port": 3002
}
```

Creation requires `app_id` and an explicit `environment` (`development`, `staging` or `production`). One project is allowed per app/environment; each has a distinct project ID and resources. Omit ports to allocate from the local policy pool. Optional `zerodowntime` must be a literal boolean: it defaults to true for production and false otherwise; true is rejected for development/staging. Existing `/docker/demo` directories cannot be adopted or overwritten. Creation installs a maintenance response and reaches `awaiting_release`; it does not start an image.

After the creation job succeeds, submit Compose YAML as a JSON string:

```json
{
  "environment": "production",
  "compose_yaml": "services:\n  app:\n    image: ghcr.io/your-org/your-app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n",
  "variables": {
    "NODE_ENV": "production",
    "DATABASE_URL": "provide-your-secret-value",
    "APP_URL": "https://demo.example.com",
    "PORT": "3000"
  }
}
```

Every new deploy requires `compose_yaml` and an explicit `environment` matching the project; image-only requests are rejected. Missing environment returns `422 DEPLOYMENT_ENVIRONMENT_REQUIRED`; a valid different environment returns `409 DEPLOYMENT_ENVIRONMENT_MISMATCH` before YAML processing or secret writes. Clients should ask the three-choice environment question before signing. The operator CLI prompts when the body omits the choice. See [deployment environments](ENVIRONMENTS.md). The YAML is limited to 64 KiB and eight services. The service validates its strict subset before passing a normalized, hardened revision to `docker compose config --quiet`; a job is accepted only after that check passes. Pull and startup operate on all services through Docker Compose. `app` uses the project's root template; companion services name their local policy using `x-dockyard-template`.

Environments may be literal string maps inside each Compose service. Alternatively, `variables` supplies the full app snapshot; it cannot be combined with `services.app.environment`. Omitting both copies the current app environment to a new revision. First deploy requires a snapshot. New environment maps require `deploy.environment`. Values are write-only, single-line, up to 8192 bytes each, and obey the service template's allowed/required keys. Dollar signs/quotes remain literal through raw env files.

The [Compose policy](COMPOSE.md) specifies all supported fields and includes a multi-service app/database example. Named volumes require single-slot mode and locally approved mount targets. Stateless stacks can use blue-green deployment. Every service must pass ownership, image, environment, health and mount/port identity checks before Caddy switches traffic to `app`.

Files include immutable `compose/cmp-<sha256>.yml` revisions, `blue.env`/`green.env` bindings, app secret files under `env/`, and private companion environment directories. After activation, `compose.yml` and `.env` mirror the release for operators. Running slots use immutable files, so candidate changes cannot alter the serving stack. Rollback retains the exact Compose/image/environment revision; persistent data remains unchanged.

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
