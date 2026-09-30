# User-submitted Compose deployments

Validated service metadata is indexed in SQLite. `/v1/projects/{id}/services` returns recorded slot/service/image/template/revision inventory without reading manifests. Actual lifecycle commands still verify immutable Compose files and root policy before invoking Docker. See [database storage](DATABASE.md).

Every new `POST /v1/projects/{id}/deploy` requires `compose_yaml` and an explicit `environment` matching its project. Choose development, staging or production before deploying; development/staging always use one Compose instance and only production permits blue-green. See [deployment environments](ENVIRONMENTS.md). The earlier image-only request is rejected. The upstream backend signs the JSON request; this VPS service independently validates the YAML before Docker sees it. Project creation reserves domains/ports and installs maintenance; deployment supplies the actual application stack.

The service first parses a bounded, strict subset of Compose entirely in memory. It then constructs an immutable, hardened Compose revision, extracts environments into private files, and runs `docker compose config --quiet` on that normalized revision before accepting a job. The worker uses `docker compose pull` and `docker compose up --wait` for every service. It verifies ownership, health, image identities, environments, mounts and published ports before Caddy sends traffic to `app`.

The repository includes parser/admission/fault tests and an installed-Compose roundtrip. Live Linux container, persistence and traffic acceptance remain required.

## Request and service policy

```json
{
  "environment": "production",
  "compose_yaml": "services:\n  app:\n    image: ghcr.io/your-org/your-app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n",
  "variables": {
    "DATABASE_URL": "provide-your-secret-value",
    "APP_URL": "https://demo.example.com"
  }
}
```

Use actual approved image digests. The JSON body retains the 128 KiB limit; `compose_yaml` is at most 64 KiB in UTF-8 bytes. Supply an app environment snapshot either in `variables` or in `services.app.environment`, never both. Omitting both copies the current app environment into a new private revision; the first deploy requires a snapshot, including an explicit empty map when no variables are required. Each companion service supplies its own environment. New environment maps require `deploy.environment` as well as `deploy.execute`.

Only these fields are accepted:

| Location | Fields and constraints |
| --- | --- |
| Top level | `services` (1–8), optional `volumes` (at most 8) |
| Every service | Required digest-pinned `image`; optional `x-dockyard-template`, `environment`, `command`, `entrypoint`, `depends_on`, `volumes`, `ports`, `expose` |
| `app` | Required service name and Caddy target. Uses the project's selected root template. |
| Companions | Require `x-dockyard-template` naming a locally approved root template. No published host ports. |
| `environment` | Mapping of literal, single-line string values. Quote numbers/booleans. Per-template allowed/required keys; at most 100 keys and 8192 bytes/value. Dollar signs remain literal; no environment interpolation. |
| `command`, `entrypoint` | Container argument arrays, at most 32 entries of 1024 bytes each. Shell-string forms are rejected. Dollar signs remain literal. These never become host shell commands. |
| `depends_on` | Service-name list, at most 8 distinct names; no missing services/cycles. Normalized to `service_healthy` dependencies. |
| `ports` | Optional for `app` only: one quoted container port or `127.0.0.1:<reserved blue port>:<container port>`. Target must match root policy. Agent substitutes the selected slot port; public bind forms are rejected. Prefer omitting this field. |
| `expose` | Optional one quoted port matching the service template. Internal connections use service names on Compose's project network. |
| Volume declaration | Named key with an empty object, for example `data: {}`. Default local driver; no external names/options. |
| Volume mount | `data:/approved/container/path[:ro\|rw]`. Target must appear in that service template's `allowed_volume_targets`. Single-slot projects only. |

Unknown fields are rejected, including build contexts, host binds, `env_file`, host namespaces/networking, capabilities/devices, privileged mode, external resources, includes/extends, file-backed secrets/configs, profiles, custom container/project names, and caller labels. YAML aliases/anchors/merges, custom tags, duplicate keys, multiple documents, excessive nesting, and oversized node counts are also rejected. Submitted YAML is never passed directly to Compose's file loader, which can otherwise read host files ([Docker trust model](https://docs.docker.com/compose/trust-model/)).

The root template supplies image repository, health command, non-root UID/GID, container port, environment policy, CPU/memory limits, and approved volume targets. The aggregate stack must fit root `max_stack_memory_mb` and `max_stack_cpus`; when omitted/zero, each defaults to the largest individual approved template limit. The sample policy explicitly permits 2048 MiB and 4 CPUs per slot. Operators must budget for both slots during overlap. Exceeding the sum returns `COMPOSE_RESOURCE_LIMIT`. Every generated service has a read-only root filesystem, bounded `/tmp`, dropped capabilities, no-new-privileges, PID/logging limits, and agent ownership labels. Images must work under this policy; implicit image-declared anonymous volumes are rejected during identity checks. Root policy changes affecting a recorded slot must be restored before operations can continue.

## Multiple services and persistent data

This single-slot example requires an operator-approved `postgres` template in addition to `web-node`:

```yaml
services:
  app:
    image: ghcr.io/your-org/your-app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    environment:
      NODE_ENV: "production"
      DATABASE_URL: "postgresql://app:example-only-secret@db:5432/app"
      APP_URL: "https://demo.example.com"
    depends_on: [db]
  db:
    x-dockyard-template: postgres
    image: ghcr.io/your-org/your-postgres@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    environment:
      POSTGRES_DB: "app"
      POSTGRES_USER: "app"
      POSTGRES_PASSWORD: "example-only-secret"
    volumes: ["data:/var/lib/postgresql/data"]
volumes:
  data: {}
```

An illustrative root template to adapt to your prepared image:

```json
{
  "image_repository": "ghcr.io/your-org/your-postgres",
  "container_port": 5432,
  "health_command": ["pg_isready", "-h", "127.0.0.1"],
  "readiness_path": "/unused-for-companion",
  "allowed_environment": ["POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD"],
  "required_environment": ["POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD"],
  "user": "999:999",
  "memory_mb": 512,
  "cpus": 1,
  "allowed_volume_targets": ["/var/lib/postgresql/data"]
}
```

The database image must initialize/run as that UID with its data in the approved volume and temporary writes in `/tmp`. An arbitrary stock image may need preparation. Only `app` receives HTTP readiness probes; all services require the configured Docker healthcheck.

Create this project with `zerodowntime: false`. Named volumes keep their Compose project identity across deploy/restart/start/rollback, and the service never removes them. Existing volumes must have matching Compose ownership labels, the local driver, and no driver options. Backups and schema migrations are operator responsibilities; rolling back containers does not roll back database data. Changing service/volume names can create new storage or leave retained resources, so keep persistent topology stable. A failed partial stack start may require local recovery before its slot can be reused.

With `zerodowntime: true`, the complete stateless stack runs under separate blue/green Compose project names. Companion services are duplicated too; workloads must tolerate overlap. Persistent volume declarations/mounts are rejected with `COMPOSE_PERSISTENT_BLUE_GREEN`. Use a separately managed shared database for stateless blue-green apps. Shared persistent-service orchestration within a blue-green project is not implemented.

## Files, rollback and logs

Immutable normalized Compose files are stored at `/docker/<id>/compose/cmp-<sha256>.yml`. They use JSON syntax, which Docker accepts as YAML. Their digest is verified before Compose commands, and every service template has a recorded policy hash. Slot bindings select the exact release's Compose and environment revisions. `compose.yml` and `.env` are operator mirrors updated after successful activation; containers use immutable files. Verified `compose.legacy.yml` preserves old recorded releases for restart/rollback.

Primary environments live at `env/env-<id>.env`; companion files are under `env/env-<id>/<service>.env`. They are private and are absent from job/audit/read responses. Logs redact retained values from every service. Select a service with `GET /v1/projects/<id>/logs?service=db&slot=active`; default service is `app`. A service must belong to the selected slot's recorded revision.

Admission retains conservative limits: 500 entries at the top of the environment directory (companion directories also count), 500 Compose revisions, and 500 activations per project. Failed staging can leave private orphan files counted toward these limits. Automatic retention and cleanup of removed services/volumes are deferred; stop preserves data and does not run Compose down or prune.
