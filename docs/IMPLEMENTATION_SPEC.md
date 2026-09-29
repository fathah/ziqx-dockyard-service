# VPS deployment agent — proposed implementation specification v1.2

Status: reviewed design proposal; implementation and staging validation remain outstanding.  
Date: 2026-09-17  
Basis: `VPS_Deployment_Agent_Implementation_Spec.md`, version 1.0.  
Companion: [review and change rationale](SPEC_REVIEW.md).

Implementation note (2026-09-29): an initial Go implementation now exists. See [STATUS.md](STATUS.md) and [API.md](API.md) for the implemented contract and remaining gaps. This specification remains the broader design target.

This document consolidates the functional contract and resolves the ambiguities identified in the source specification. The intended architecture remains a native Go agent on each Linux VPS, managed by systemd, controlling locally allow-listed Docker Compose services behind host Caddy. It is a proposed implementation target, not evidence of production readiness.

The requested hostname-upsert API, template-based site creation, and single-slot mode are specified in [CADDY_SITE_API.md](CADDY_SITE_API.md), which extends this core contract. Its explicit exceptions govern generated-project initialization and `zerodowntime: false`. One application uses one parameterized Compose file with one web service; blue and green are separate Compose project instances of that file.

## 1. Scope and guarantees

The agent supports immutable GHCR image deployments, health-gated blue-green switching, rollback, start/stop, rolling restart, write-only environment revisions, bounded logs, persistent jobs, and attributable audit records. The existing Super Admin backend owns human authentication, RBAC, request signing, and the UI.

Normal deployment keeps the previous container serving during pull, candidate startup, and readiness checks. A successful Caddy reload sends new requests to the candidate; the previous container remains alive through the configured drain period. The guarantee covers ordinary requests that finish within that period. Infinite streams, WebSockets, host failure, application defects after activation, and incompatible database migrations are outside the guarantee.

V1 does not build images, schedule across hosts, execute arbitrary commands, proxy Docker APIs, accept remote host paths or service names, expose container terminals, or automatically reverse database migrations. Hard restart of a blue-green project and live root-policy reload are deferred. Restart uses blue-green when the site's mode is `zerodowntime: true`; single-slot operation explicitly permits downtime as defined in the site extension. Application web services must tolerate overlapping versions, handle SIGTERM, and avoid duplicate singleton schedulers. Shared databases and queues stay outside the managed slots.

## 2. Components and trust

```text
Super Admin UI → Super Admin backend → HTTPS with mTLS → host Caddy
                                                        ↓ loopback
                                                   Go agent
                                               ↙      ↓       ↘
                                        Compose CLI  SQLite  local files
```

- Caddy requires a client certificate under a dedicated control-plane CA and forwards to `127.0.0.1:9123`.
- Every `/v1/*` request also requires HMAC authentication and an allowed signed scope. `/healthz`, `/readyz`, and metrics remain local or behind the same mTLS route.
- Only the backend holds the client private key and per-VPS HMAC keys. The browser never receives either.
- Root-owned local configuration chooses all executable paths, Compose projects/services, file paths, image repositories, probes, and routing targets.
- Docker access is root-equivalent. A non-root account with Docker privileges is not treated as a security boundary.
- Caddy's admin endpoint must be local and protected, preferably a permissioned Unix socket. The agent reads it for reconciliation and invokes the configured Caddy binary for validation/reload. It never exposes this endpoint through its public API.
- Forwarded certificate identity and source IP are trusted only when Caddy overwrites the relevant headers. They are audit attributes, not substitutes for signature verification. Requests directly from loopback are not automatically authorized.

## 3. Configuration and startup

Configuration is strict JSON at `/etc/deploy-agent/config.json`, root-owned, mode `0600`. Reject unknown fields, trailing JSON, duplicate IDs, malformed URLs, unsafe paths, and invalid or unbounded timeout/concurrency settings. Production startup verifies ownership, symlink policy, and parent directory permissions, including referenced Compose configuration and environment files.

Example field contract (placeholders must be replaced for an actual VPS):

```json
{
  "server_id": "vps-01",
  "listen_addr": "127.0.0.1:9123",
  "state_database": "/var/lib/deploy-agent/state.db",
  "audit_log": "/var/log/deploy-agent/audit.jsonl",
  "lock_file": "/var/lib/deploy-agent/agent.lock",
  "docker_binary": "/usr/bin/docker",
  "docker_config_directory": "/etc/deploy-agent/docker",
  "caddy_binary": "/usr/bin/caddy",
  "caddy_config": "/etc/caddy/Caddyfile",
  "caddy_admin_address": "unix//run/caddy/admin.sock",
  "global_max_concurrent_jobs": 2,
  "max_queued_jobs_per_project": 20,
  "max_queued_jobs_total": 100,
  "request_body_limit_bytes": 131072,
  "command_timeout_seconds": 120,
  "log_max_bytes": 2097152,
  "log_max_since_seconds": 86400,
  "auth": {
    "active_hmac_key_id": "control-2026-01",
    "hmac_keys_env": "DEPLOY_AGENT_HMAC_KEYS_JSON",
    "max_clock_skew_seconds": 60,
    "idempotency_retention_hours": 24
  },
  "projects": [
    {
      "id": "myapp",
      "display_name": "My Application",
      "compose_service": "app",
      "compose_file": "/opt/apps/myapp/compose.yml",
      "project_directory": "/opt/apps/myapp",
      "environment_history_directory": "/var/lib/deploy-agent/env/myapp",
      "allowed_environment_keys": ["DATABASE_URL", "APP_URL", "NODE_ENV"],
      "required_environment_keys": ["DATABASE_URL", "APP_URL"],
      "secret_environment_keys": ["DATABASE_URL"],
      "allowed_image_prefix": "ghcr.io/your-org/your-app@sha256:",
      "live_upstream_symlink": "/etc/caddy/upstreams/myapp-live.caddy",
      "maintenance_upstream_file": "/etc/caddy/upstreams/myapp-maintenance.caddy",
      "deploy_timeout_seconds": 600,
      "health_timeout_seconds": 180,
      "drain_seconds": 90,
      "stop_timeout_seconds": 60,
      "retain_releases": 10,
      "smoke_probes": [],
      "blue": {
        "compose_project_name": "deploy-myapp-blue",
        "compose_bindings_file": "/var/lib/deploy-agent/bindings/myapp-blue.env",
        "host_port": 3001,
        "health_url": "http://127.0.0.1:3001/health/ready",
        "upstream_file": "/etc/caddy/upstreams/myapp-blue.caddy"
      },
      "green": {
        "compose_project_name": "deploy-myapp-green",
        "compose_bindings_file": "/var/lib/deploy-agent/bindings/myapp-green.env",
        "host_port": 3002,
        "health_url": "http://127.0.0.1:3002/health/ready",
        "upstream_file": "/etc/caddy/upstreams/myapp-green.caddy"
      }
    }
  ]
}
```

Startup must:

1. Acquire an exclusive process lock; only one agent or local bootstrap process may control the state directory.
2. Validate root-owned executables and config, loopback-only listening, distinct slot project names/ports, globally unique Compose project names and non-overlapping agent-owned paths across applications. The service name inside the shared Compose file is deliberately the same for both slot projects.
3. Require exact `ghcr.io/<repository>@sha256:` prefixes and lowercase 64-hex digests. Required and secret environment keys must be subsets of the allowed keys. Environment names match `[A-Za-z_][A-Za-z0-9_]*` and cannot collide with agent-controlled Compose binding variables.
4. Validate state, environment, and audit permissions. Secret directories are `0700`, secret files `0600`; Caddy routing files contain no secrets and must be readable by the Caddy service account. Configured executable and directory ancestors must not be writable by untrusted users.
5. Load one or two key IDs with distinct base64-encoded keys decoding to at least 32 random bytes. The active key must exist. The Node client and Go verifier share the same encoding contract.
6. Initialize/migrate SQLite transactionally, then reconcile interrupted work before accepting mutations.
7. For initialized projects, run bounded Compose configuration checks separately with each slot's persisted bindings, and validate Caddy. For uninitialized projects, verify static files and maintenance routing; defer rendered Compose validation until local bootstrap or an authorized generated-project first deploy supplies digest/environment bindings.
8. Verify the live symlink resolves exactly to the configured blue, green, or maintenance file. Fail startup for structurally unsafe config; report runtime unavailability/divergence through readiness and status while keeping safe authenticated diagnostic reads available.

Pin tested Go, Docker Engine, Compose, and Caddy versions during implementation. Detect unsupported tool versions or required flags at startup; do not silently weaken health checks. V1 config changes require a controlled agent restart.

## 4. Compose and environment bindings

Use one parameterized Compose file with one managed web service. Run it under two distinct Compose project names for blue-green, or one project name for single-slot mode. The file does not change during normal deployments. Use explicit `--project-name`, `--project-directory`, `--env-file`, and `-f` arguments for every Compose invocation. Each slot has its own agent-owned bindings file containing `APP_IMAGE` (immutable digest), `APP_HOST_PORT` (reserved loopback port), and `APP_ENV_FILE` (agent-generated immutable environment snapshot path). Atomically update only the target slot's bindings; never include application environment values in those bindings. Supply a minimal child environment and no ambient `COMPOSE_*` overrides or unrelated secrets.

```yaml
services:
  app:
    image: ${APP_IMAGE:?APP_IMAGE is required}
    restart: unless-stopped
    env_file:
      - ${APP_ENV_FILE:?APP_ENV_FILE is required}
    ports:
      - "127.0.0.1:${APP_HOST_PORT:?APP_HOST_PORT is required}:3000"
    healthcheck:
      test: ["CMD", "curl", "-fsS", "http://localhost:3000/health/ready"]
      interval: 5s
      timeout: 3s
      retries: 24
      start_period: 20s
    stop_grace_period: 60s
```

Conceptual invocations (the implementation passes argument arrays, not shell strings):

```text
docker compose -p deploy-myapp-blue --project-directory /opt/apps/myapp --env-file /var/lib/deploy-agent/bindings/myapp-blue.env -f /opt/apps/myapp/compose.yml up -d --no-deps --no-build --force-recreate --wait app
docker compose -p deploy-myapp-green --project-directory /opt/apps/myapp --env-file /var/lib/deploy-agent/bindings/myapp-green.env -f /opt/apps/myapp/compose.yml up -d --no-deps --no-build --force-recreate --wait app
```

These illustrate the same file/service with different project identities and bindings. The actual runner adds configured wait timeouts and starts only the required slot at each phase. Separate Compose projects isolate default networks and named volumes, so shared databases must be external services reached through approved networking, not duplicated dependencies or implicitly shared per-project volumes. [Compose project names](https://docs.docker.com/compose/how-tos/project-name/)

The application image must contain the health-check executable. Reject disabled or missing health checks. Each slot is exactly one container, has a distinct loopback host port, and has no `container_name`, build instruction, Docker socket mount, or privileged/host-namespace access. Treat referenced Compose includes, extensions, bind mounts, and environment precedence as part of the trusted configuration; validate the effective model. Managed web services must not override revision-managed keys using Compose `environment` entries. Do not create or stop shared database/queue dependencies.

Persist each slot's release, digest, environment revision, and resolved local image ID together. Verify the pulled digest and container image ID; do not equate a multi-platform registry index digest directly with Docker's local image ID. Labels can aid reconciliation, but labels alone do not prove actual image identity. Candidate preparation changes only the inactive slot binding. No mutable tag fallback is allowed.

Environment revisions are complete replacements, not implicit patches. Missing required keys return `422`; unknown keys or invalid values return `400`. Empty strings are distinct from omitted keys. Reject NUL and values exceeding documented body/per-value limits. Support literal dollar signs, quotes, backslashes, whitespace, newlines, and `#` with a Compose-compatible dotenv encoder tested by round-tripping through real containers. No shell sourcing is permitted.

Write snapshots to agent-generated paths using exclusive temporary creation, mode `0600`, file fsync, atomic rename on the same filesystem, and parent-directory fsync. The API cannot choose names or paths. Files are immutable once referenced. A crash may leave an unreferenced file; cleanup may remove it only after confirming no metadata or in-progress operation references it.

## 5. Signed request protocol

All `/v1/*` requests require these headers:

```text
X-Deploy-Key-ID
X-Deploy-Timestamp
X-Deploy-Scopes
Idempotency-Key
X-Actor-ID
X-Request-ID
X-Deploy-Signature
```

Signing input is UTF-8, with LF between fields and no trailing LF:

```text
deploy-agent-v1
serverID
keyID
timestamp
idempotencyKey
actorID
requestID
canonicalScopes
HTTPMethod
exactRequestTarget
lowercaseHex(SHA256(rawBody))
```

`serverID` is the expected configured VPS ID, bound into the signature even though it is not a request header. Use a separate key set per VPS. `exactRequestTarget` is the escaped path plus `?` and the exact raw query string when present. Sign the transmitted bytes; the client must not reserialize JSON or reorder the query afterward. Caddy must preserve the target. Reject ambiguous/noncanonical paths, encoded path separators, duplicate security headers, duplicate/unknown query keys, malformed IDs, and line breaks or control characters in signed fields. Define bounded ID lengths and shared positive/negative test vectors before either client is implemented.

`canonicalScopes` is the sorted, unique, single-space-separated set of scope names. Reject noncanonical header representations. Verify HMAC-SHA256 in constant time, require lowercase 64-hex signatures, and enforce absolute clock skew of at most 60 seconds. A retry may use a new timestamp/signature and rotated key, while retaining the operation's idempotency key, request ID, actor, target, scope set, and exact body.

| Capability | Required scope |
| --- | --- |
| Projects, status, releases, jobs, environment key metadata | `deploy.read` |
| Application logs | `deploy.logs` |
| Deploy and rolling restart | `deploy.execute` |
| Rollback | `deploy.rollback` |
| Stage environment revision | `deploy.environment` |
| Create environment revision and deploy | Both `deploy.environment` and `deploy.execute` |
| Start | `deploy.lifecycle` |
| Confirmed stop | `deploy.stop` |

The backend checks per-project human permissions before signing; the agent checks the signed scope against the operation. All valid control-plane keys are trusted to assert scopes, so this is not protection against a compromised signing backend.

Mutation idempotency uses a separate durable table with a unique key per server. Fingerprint the stable operation identity: protocol version, server, actor, request ID, method, exact request target, canonical scopes, and raw body. Exclude timestamp, key ID, and signature. For requests containing environment values, persist a keyed fingerprint using a separate stable local fingerprint secret so the DB does not expose an unkeyed hash usable for low-entropy secret guessing. Preserve that secret in protected backups; it does not rotate with transport keys.

Atomically create the job and idempotency record. Same key and stable request identity returns the original stored `202` response; changed identity returns `409 IDEMPOTENCY_CONFLICT`. Authenticate and authorize every retry before looking up its response. A different key reusing a request ID returns `409 REQUEST_ID_CONFLICT`. Retain records at least 24 hours after acceptance and never expire those attached to nonterminal jobs. After expiry, callers must not assume a retry is deduplicated. Unknown outcome is resolved by polling the recorded job.

Read requests require fresh signed request metadata but are not persisted as mutation jobs or cached responses. Bounded replay within the clock window is allowed for authenticated reads; do not promise exactly-once reads or require a nonce database for every poll. This explicitly replaces the source's ambiguous “fresh idempotency key on every request” acceptance wording.

## 6. Durable state, acceptance, and audit

SQLite uses WAL, foreign keys on every connection, explicit transactions, a busy timeout, a documented connection policy, and `synchronous=FULL` for durability-sensitive writes. No transaction stays open during Docker, Caddy, health probes, or drain waits.

Required entities:

| Entity | Required data and invariants |
| --- | --- |
| `projects_state` | Project ID, initialized flag, desired state, active release/slot nullable for uninitialized state, observed route, reconciliation status, updated time. |
| `slot_bindings` | Unique project/slot, intended release/digest/environment revision, observed container and image IDs, generation and operation association. |
| `releases` | Immutable image/environment identity, project, original actor, source (`bootstrap`, `deploy`, `rollback`, `restart`, `env_apply`), previous/target release links, activation time, lifecycle status, safe failure code. |
| `jobs` | Unique job/request IDs, project, sequence, action, safe request metadata, durable execution inputs, status, phase, safe result/error, created/started/finished times. |
| `job_events` | Append-only phase changes, timestamps, safe progress codes; ordered per job. |
| `idempotency_records` | Unique operation key, stable keyed fingerprint, associated job, original HTTP response, retention deadline. |
| `environment_revisions` | Project, monotonic version, generated snapshot path, key names/secret flags, creator/time; never values. |
| `route_switches` | Job/project, previous and candidate route/release, generation, durable intent, observed outcome, confirmation time, drain deadline. |
| `audit_events` | Monotonic event ID, server/actor/request/job/project/action, previous/new safe state, outcome, trusted source attributes, safe code, JSONL delivery progress. |

Job states: `queued`, `running`, `succeeded`, `failed`, `cancelled`, `recovery_required`. Cleanup failures after activation are a successful result with warning codes. `recovery_required` blocks further mutations for that project until a verified recovery resolves it; an unknown global route state also blocks shared route changes.

Durable execution inputs include resolved immutable image/environment/release IDs and confirmation data, not just a redacted prose summary. Workers can resume classification after a crash without reconstructing secrets from a request log. Resolve omitted rollback/restart targets at acceptance and store them; revalidate execution preconditions at dequeue. Stage-only environment work still takes its place in the project's durable mutation order.

Every mutation returns `202` only after acceptance is durable. Environment creation first makes the immutable snapshot durable; a transaction then assigns its version, creates metadata, and stores the job/idempotency response. This may leave a safe orphan file on a pre-commit crash, but must never leave an accepted job with missing secret input. Validate and bound the queue before costly acceptance work, and handle races with database uniqueness/transactions. A staged revision becomes available to other jobs only after its staging job succeeds; its own env-apply job may use it under the same project lock.

Write audit events in the same DB transaction as corresponding job/state transitions. Export JSONL from a transactional outbox, with fsync before acknowledging delivery. A crash may duplicate a line; stable event IDs allow deduplication. Do not claim atomicity across SQLite and JSONL. If durable DB audit cannot be written, reject new mutations. JSONL failure raises readiness/metrics alerts and triggers a configurable bounded-backlog admission limit; never tear down a serving container solely because export is unavailable.

Audit accepted operations, authentication/authorization rejections, phase transitions, recovery decisions, and outcomes. Do not trust actor claims from unauthenticated requests. Bound and aggregate abusive rejection logging with counters to avoid unbounded storage exhaustion. Never include request bodies containing variables, raw process output, signatures, credentials, or environment values.

## 7. Scheduling and process execution

- Use a durable FIFO order per project, including environment staging; only one mutation executes per project.
- Fairly schedule the next eligible job across projects, respecting a global limit. A worker waiting for one project's mutex must not occupy capacity needed by another project.
- Hold a separate host-wide route lock for shared Caddy changes. Use a consistent lock order: process lock → project ownership → route lock. Manual routing procedures must stop the agent or coordinate the same lock.
- Bound queues globally and per project; return `429 QUEUE_FULL` with a safe retry hint. Backend mutation rate limits remain required.
- Use `exec.CommandContext` with absolute executables and argument slices. No shell, `eval`, user-supplied executable, or arbitrary argument passthrough.
- Bound stdout/stderr in memory while continuing to drain pipes. Never return raw output. Persist diagnostics only when proven sanitized; Compose config/inspect output can contain environment secrets and must not be dumped to disk.
- On Linux, place child processes in a process group and terminate the group on timeout/cancellation. Verify this behavior in Linux tests, including grandchildren and pipe closure.
- Use explicit Docker credential config/helper access. Preserve only required safe variables such as a fixed PATH, locale, and configured Docker config path. Never inherit the agent HMAC secret.
- Apply bounded command, HTTP, header, idle, deployment, and probe timeouts. Health probes target configured loopback endpoints and disable redirects; optional smoke probes are bounded and configured locally, never supplied by requests.

Candidate startup includes `up -d --no-deps --no-build --force-recreate --wait --wait-timeout <bounded> <configured-service>`. Verify the effective health check and Docker healthy status separately. Always explicitly select the candidate slot's project name/bindings and the configured service. The same service name in the other slot's project must remain untouched. Retention never invokes broad Docker prune, Compose down, remove-orphans, or volume deletion.

## 8. Bootstrap and project lifecycle

An uninitialized project is maintenance-routed with no active release. For locally configured projects, remote deploy/restart/start/rollback return `409 PROJECT_UNINITIALIZED`. Generated sites permit a first remote deploy under the site extension; restart/start/rollback still require a recorded release. Read APIs remain available. Environment revisions may be staged remotely after the agent starts.

Provide a local administrative bootstrap command with an allowed project ID, immutable image reference, and existing revision ID or a protected local environment input file. It shares the agent's validation and process lock and must not run alongside the daemon. This is a local provisioning interface, not an HTTP path-selection API. It records a local administrator actor and request ID.

Bootstrap initializes both Compose bindings to a valid digest/environment pair, starts only blue, verifies health and optional smoke checks, then uses the normal journaled route switch from maintenance to blue. Persist initialized/running state only after verified activation. If it fails, keep maintenance and retain evidence for local recovery. Bootstrap does not claim zero downtime before a first healthy release exists.

Stop requires `confirmation` exactly equal to the project ID. It records stop intent, switches to the configured maintenance response under the route lock, waits a drain period for previous ordinary requests, then gracefully stops both slots. Retain the active release identity for a later start. Desired and observed state are reported separately; a failed cleanup may leave containers alive while maintenance continues serving.

Start restores the recorded active image/environment pair, waits for health, switches from maintenance, and records running state. Start requires an initialized project. Repeated start of an already healthy running project and repeated stop of an already maintenance-routed stopped project produce attributable successful no-op jobs. A new idempotency key does not justify recreating a healthy app for a no-op.

## 9. Deployment and routing state machine

Normal deploy requires initialized/running state, an allowed digest, a ready environment revision belonging to the project, and reconciled route/container evidence. Treat the inactive slot as reusable only after any previous drain has completed and its remaining container is safely stopped. Never reuse a slot still referenced by an unresolved switch or drain.

1. Claim the persisted job and project ownership; record `validating`.
2. Reconcile stored state, both bindings, Docker identity/health, disk route target, and live Caddy route. Stop on divergence.
3. Persist a candidate release and slot preparation intent. Durably update only the inactive binding, then record its generation. A crash between file and DB changes is classified through the intent record.
4. Pull the allowed digest, verify image identity, force-recreate only the inactive service, and verify Docker health plus the configured readiness endpoint. Record phase events for pulling, starting, readiness, and optional smoke testing.
5. On pre-switch failure, preserve the active binding and route. Stop only a positively identified failed candidate when safe. Record failure; uncertain ownership requires reconciliation, not broad cleanup.
6. Acquire the global route lock. Re-read live state and candidate health. Persist and fsync the route-switch intent with old/new targets before changing the link.
7. Create a temporary symlink beside the live link, atomically rename it, and fsync the directory. The target must be one of the project's three configured files.
8. Run bounded Caddy validation and reload. Determine the active project route from Caddy's running configuration and verify it matches the intended target. Do not infer activation from the symlink or a probe of the candidate's direct port.
9. After verified activation, transactionally mark the candidate active, prior release inactive, record switch confirmation and a persistent drain deadline, and advance the job phase. Release the global route lock before draining.
10. Keep the project mutation ownership while draining. Do not stop the previous slot before the deadline. After restart or clock uncertainty, waiting a fresh full drain interval is an acceptable conservative fallback.
11. Recheck the live route and current candidate health before old-slot cleanup. If the new active slot is unhealthy, retain the old slot and surface degradation; v1 does not automatically redirect traffic without an explicit recovery policy.
12. Stop the previous slot gracefully, perform only reference-safe retention, and finish the job. Cleanup failure after confirmed activation yields success with warnings and an exposed, idempotent cleanup retry through a local administrative operation.

On validation failure before reload, restore the old link durably and verify expected live state. On an explicit rejected reload, restore disk intent and verify the prior route still serves. On timeout, transport failure, agent interruption, or unverified reload outcome, keep both containers alive, inspect live Caddy configuration under the route lock, and classify actual state. If the candidate is active and healthy, finish recording activation and drain; if the prior route is active, compensate disk intent; otherwise mark `recovery_required`. Never blindly restore a symlink and stop the candidate after an ambiguous reload.

If Caddy applied a switch but SQLite commit failed, preserve both containers and the durable intent. Recovery must finish or classify the switch before any further route mutation. No automatic traffic rollback follows a cleanup failure.

Use a locally validated route-observation mapping per app hostname/upstream or stable Caddy route identity. An ambiguous match is divergence. Agent-managed Caddy routing has a single writer; unrelated operator changes must use the coordinated maintenance procedure. Caddy reloads affect the entire host configuration, so test cross-project failure and concurrency explicitly.

The maintenance file returns a fixed `503` with an appropriate `Retry-After`. Those responses are intentional during stop/initialization and are excluded from deployment zero-downtime acceptance measurements.

## 10. Recovery and shutdown

At startup and before each mutation, inspect persistent intent, actual Docker containers and image identity, environment bindings, the recognized on-disk route, and Caddy's live configuration. A symlink alone is never the source of truth for active traffic.

| Observation | Required behavior |
| --- | --- |
| Stored state, live route, image/environment binding, and health agree | Continue. |
| Durable switch intent exists and live candidate route/identity are proven | Complete activation metadata and conservatively resume drain. |
| Switch intent exists, old route is still active, and candidate never received traffic | Compensate prepared disk state; fail interrupted job safely. |
| Disk and live route disagree without attributable intent | Reject mutations as `STATE_DIVERGED`; expose diagnostics. |
| Old job interrupted before any route mutation | Inspect effects, stop only a positively identified unused candidate if safe, and mark interrupted failure rather than automatically rerunning it. |
| Both slots healthy | Use proven live route plus intent and matching binding evidence, never health alone. |
| Active slot unhealthy, inactive healthy | Report degraded; no automatic route change. |
| Neither slot healthy | Report unavailable; retain evidence. |
| Caddy unavailable or route outcome cannot be established | Preserve both slots, block unsafe mutations, require recovery. |
| A queued job has durable valid inputs and no execution intent | Leave queued and execute once normal recovery is complete. |

SIGTERM/SIGINT stops request acceptance and claiming new work, allows bounded read completion, and cancels noncritical work. An in-progress route change must finish a safe boundary within a separate shutdown deadline or retain a recovery-required intent. Do not stop serving application containers simply because the agent is stopping. Drain/cleanup may resume after restart. Close SQLite after workers stop. Define recovery expectations for every persisted phase and test kills immediately before and after each side effect.

## 11. Operations and HTTP contract

All errors use `{ "error": { "code": "...", "message": "...", "request_id": "..." } }`. Messages are fixed or assembled from safe allow-listed metadata. Never echo rejected variable values, signatures, raw command output, or client-controlled strings into errors.

| Method/path | Semantics |
| --- | --- |
| `GET /v1/projects` | Safe configured project metadata and summary state. |
| `GET /v1/projects/{project}` | Safe project information; no host filesystem paths or values. |
| `GET /v1/projects/{project}/status` | Desired/observed state, active release/digest/revision, both container states/health, queue/current job, route reconciliation, observation timestamp. |
| `GET /v1/projects/{project}/releases?limit=20` | Retained release history; clamp/reject with documented maximum 100. |
| `POST /v1/projects/{project}/deploy` | Exact allowed `image`, ready `environment_revision_id`, optional bounded `reason`. |
| `POST /v1/projects/{project}/rollback` | Optional retained `release_id` and reason; deploy the exact image/environment pair as a new release event. |
| `POST /v1/projects/{project}/restart` | Optional `mode: rolling` and reason; unknown/hard modes rejected. |
| `POST /v1/projects/{project}/start` | Optional reason; restore recorded release from stopped state. |
| `POST /v1/projects/{project}/stop` | Required exact project-ID `confirmation` and optional reason. |
| `GET /v1/projects/{project}/environment` | Active revision key metadata, never values. |
| `GET /v1/projects/{project}/environment/revisions` | Paginated revision metadata and staging status, never values. |
| `POST /v1/projects/{project}/environment/revisions` | Full `variables` map, `mode: stage_only` or `deploy`, optional reason. |
| `GET /v1/jobs/{job}` | Safe status, phases/timestamps, warnings, results; project scope checks in backend. |
| `GET /v1/projects/{project}/logs` | Bounded snapshot with validated query parameters. |

Mutation response, persisted verbatim for retries:

```json
{
  "job_id": "job_example",
  "status": "queued",
  "project_id": "myapp",
  "action": "deploy"
}
```

Environment creation uses the same `202` envelope with `action` equal to `env_stage` or `env_apply`; it additionally returns reserved `revision_id`, `version`, and safe key metadata. Polling the job establishes whether staging/application succeeded. Failure to deploy does not delete the immutable revision; it remains staged for inspection or a future allowed deployment.

Rollback without a target selects the successful activation immediately preceding the current active release in that project's activation history, even if the current release is itself a rollback. Store that selected target at acceptance. Explicit targets must have been successfully activated, retained, and belong to the same project. A rollback always creates a new release and must not rewrite history. Require backward-compatible expand-and-contract database changes; no migration execution API is added.

Log parameters: slot `active|inactive|blue|green`, tail default 200/max 2000, since default 30m/max configured 24h, response cap default 2 MiB. Reject negative/invalid durations, duplicate parameters, unknown fields, arbitrary service names, and unavailable active/inactive mappings. Return timestamps and explicit truncation metadata. Strip ANSI/control escape sequences except safe line formatting. Redact known retained environment values before truncation, with boundary-safe processing and fixed markers.

The agent guarantees its own generated logs, errors, audit, metadata, and metrics do not serialize stored environment values. Application logs can contain transformed or unrelated secrets the agent cannot recognize. Document this limitation, enforce the separate log scope, and require applications to avoid secret logging. Do not label best-effort log redaction a universal secrecy guarantee.

HTTP status conventions: `400` malformed/unknown input; `401` authentication/freshness failure; `403` insufficient scope; `404` missing safe resource; `409` conflicting idempotency/request identity, stopped/uninitialized project, or divergent state; `413` body too large; `422` missing required variables; `429` full queue; `503` unavailable dependencies/admission. Accepted job failures are represented on the job resource, not by changing the original HTTP response.

Keep the source specification's stable error codes and add `SCOPE_REQUIRED`, `REQUEST_ID_CONFLICT`, `PROJECT_UNINITIALIZED`, `QUEUE_FULL`, `JOB_INTERRUPTED`, `RECOVERY_REQUIRED`, `AUDIT_UNAVAILABLE`, and `ENV_VALUE_INVALID`. Define safe messages centrally. If an old slot cannot stop after activation, return `OLD_SLOT_STOP_FAILED` as a warning in the successful job result.

## 12. Retention, observability, and packaging

Retention is reference-based. Never delete an active release, either slot's referenced environment/image, a queued job input, a rollback target reserved by a job, a drain participant, unresolved recovery evidence, or an unexpired idempotency response. Keep at least the configured count of successful activations plus all protected references. Environment deletion follows reference checks. Docker image deletion is deferred unless exclusive ownership can be proven; no volume cleanup API exists.

Expose process health, dependency readiness, and bounded metrics for job counts/durations, pull/readiness duration, failures, route changes, queue depth, active jobs, audit backlog, reconciliation failures, and build version. Readiness uses cached bounded checks so probes cannot saturate Docker. Degraded projects are explicit in status; health endpoints return no secrets or full configuration. No secret or unbounded actor/request values in metric labels.

Deliver a hardened root systemd unit with `UMask=0077`, `NoNewPrivileges`, private temporary directory, kernel/control-group protections, address-family restrictions, restart policy, bounded stop timeout, and correctly scoped filesystem access. Validate required Docker credentials, Caddy socket access, and writable locations under that exact unit. Apply filesystem sandboxing only with tested path exceptions. Installation must never overwrite an existing app's route or Compose config without an explicit migration step.

Deliver examples for configuration, a single parameterized Compose file reused by both slot projects, blue/green/maintenance Caddy files, mTLS agent hostname, systemd, and a Node/TypeScript backend client. The client uses normal server certificate verification, mTLS, identical signing vectors, stable mutation identity on retry, and bounded job polling. Its application logs must exclude variables and authorization material.

Runbooks must cover installation/bootstrap, adding projects, version compatibility, GHCR credentials, mTLS issuance/rotation/emergency trust replacement, HMAC rotation, state divergence, manual Caddy recovery, WAL-consistent SQLite backup/restore with environment snapshots and fingerprint key, agent upgrade/rollback, and credential compromise. Avoid copying only `state.db` during active WAL writes. Document a tested revocation method rather than assuming CA trust checks consult revocation lists.

## 13. Implementation phases and acceptance

Use small Go packages for config, auth, API, state, audit, jobs, deployment, environment, process execution, Docker, Caddy, and logs. Keep `main` limited to wiring, startup, and shutdown. Privileged adapters need injectable interfaces and fault-injection points. Prefer the standard library; choose and pin a maintained SQLite driver during implementation and document its build/CGO tradeoff.

| Phase | Deliverables | Exit condition |
| --- | --- | --- |
| 1 — Foundation | Strict config, migrations, signing/scope contract, idempotency, audit outbox, read APIs, runner, job admission/scheduling | Unit tests prove authentication boundaries, persistence, retry behavior, queue bounds, and no secret serialization. |
| 2 — Deployment | Bootstrap, slot binding, pull/start/health, route journal and lock, drain, reconciliation | Fault-injection and Linux integration tests prove pre-switch preservation and conservative handling of unknown outcomes. |
| 3 — Operations | Rollback, rolling restart, stop/start, environment modes, logs, reference-safe retention | Exact image/environment rollback, lifecycle confirmation, byte limits, and cleanup reference protection are demonstrated. |
| 4 — Production validation | mTLS, packaging, operational documentation, metrics, load/crash/soak tests | Recorded staging evidence satisfies every criterion below. |

Required automated coverage includes config ownership/path checks; image allow-listing; Go/Node signing vectors including query tampering and key rotation; clock skew; mutation retries with renewed timestamps; secret-safe fingerprints; real Compose dotenv round-trips; command argument allow-lists; process-group cancellation; Caddy link replacement and ambiguous reloads; cross-project route races; bootstrap/maintenance/start/stop; FIFO fairness and concurrency; retention; audit outbox recovery; shutdown at every phase; and bounded/redacted logs.

Use a disposable Linux integration environment with real Caddy, Docker/Compose, a dedicated test CA/client certificate, two tiny HTTP app images, configurable readiness failure/delay, and a long-request endpoint. Test both deployment directions, pull/start/health failures, invalid mTLS/HMAC, rollback, environment non-disclosure, drain completion, per-project serialization, global limits, interrupted acceptance, and process/host restart recovery. Tests against fake command runners supplement but do not replace these checks.

V1 completion requires:

- [ ] Public agent hostname rejects missing/untrusted client certificates; backend verifies the VPS certificate.
- [ ] Agent listener and admin surfaces remain local/protected; all `/v1/*` operations require the specified signature/scope.
- [ ] No HTTP input can select commands, host paths, Compose services, or unapproved registries.
- [ ] Local bootstrap works and persists an exact digest/environment pairing.
- [ ] Unhealthy or incorrectly bound candidates never receive traffic.
- [ ] Pre-switch failures preserve the serving release and its environment binding.
- [ ] Parallel projects cannot load one another's uncommitted Caddy changes.
- [ ] Ambiguous route outcomes preserve containers and block unsafe follow-up mutations.
- [ ] Ordinary in-flight requests complete within drain during deployment.
- [ ] Restart is rolling for blue-green sites; single-slot downtime is explicit; stop requires confirmation and maintenance routing; start restores the recorded release.
- [ ] Rollback activates a retained exact image/environment pair as a new event.
- [ ] Environment values remain write-only; documented application-log limitations are tested and disclosed.
- [ ] Jobs and idempotency survive crashes; retries cannot duplicate an accepted mutation within retention.
- [ ] All API operations, bounded logs, status, and health/readiness work.
- [ ] Audit is attributable, durable, bounded under abuse, and recoverable after JSONL write failure.
- [ ] Process/VPS restart recovery is demonstrated at every critical transition.
- [ ] `go vet ./...` and meaningful unit/integration race tests pass; Linux cancellation tests pass on Linux.
- [ ] Reproducible Linux builds are recorded for supported architectures with pinned dependencies/toolchain.
- [ ] At least 100 alternating deployments under continuous ordinary traffic show no deployment-related 5XX or intentionally interrupted in-drain requests.
- [ ] All operational runbooks are exercised in staging, including backup/restore and credential replacement.

Build the backend agent and state machine before adding large Super Admin screens. Domain names, VPS architecture, application probes, ports, secrets, version pins, and registry organization are installation inputs. Capacity for two live application versions and backward-compatible shared data access are application prerequisites.

## 14. Technical references

- [Caddy active configuration, replacement, and concurrency](https://caddyserver.com/docs/api)
- [Caddy reload command](https://caddyserver.com/docs/command-line#caddy-reload)
- [Caddy mTLS and trust pools](https://caddyserver.com/docs/caddyfile/directives/tls)
- [Docker Compose up options](https://docs.docker.com/reference/cli/docker/compose/up/)
- [Compose interpolation and literal values](https://docs.docker.com/compose/how-tos/environment-variables/variable-interpolation/)
- [Compose environment-file contract](https://docs.docker.com/reference/compose-file/services/#env_file)

These references establish tool behavior. The locking, journal, idempotency, and failure policies above are project design decisions derived from that behavior and require implementation tests.
