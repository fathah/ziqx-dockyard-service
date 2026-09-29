# Caddy hostname upsert and application configuration

Status: proposed API extension, requested during specification review.  
Date: 2026-09-17  
Read with [IMPLEMENTATION_SPEC.md](IMPLEMENTATION_SPEC.md).

Implementation note (2026-09-29): the first version implements project creation and project route updates rather than this proposed hostname-upsert endpoint. See [API.md](API.md) for the current paths and [STATUS.md](STATUS.md) for deferred work.

The user requested creation or update of a Caddy hostname through a POST body, including a port and `zerodowntime: true|false`. This extension intentionally permits managed site creation through a narrow typed API. It does not permit arbitrary Caddy text, shell commands, host paths, or Docker Compose YAML.

The proposed design preserves one Compose file with one managed app service. Blue-green runs that same file twice under separate Compose project names with separate port/image/environment bindings. Caddy changes the active route; Docker Compose starts and stops the two app instances. A Caddy-only change cannot provide blue-green deployment when only one app instance exists.

Prepare or generate the parameterized Compose file once from a root-owned template. There is no need to rewrite it on each deployment. Existing files with fixed ports, a fixed `container_name`, or in-project singleton dependencies need a one-time preparation step before enabling blue-green; the API must not silently rewrite arbitrary existing Compose files.

## 1. Endpoint and request

```http
POST /v1/sites/example.com
Content-Type: application/json
```

```json
{
  "port": 3001,
  "zerodowntime": true,
  "secondary_port": 3002,
  "template_id": "web-node",
  "reason": "Configure the application domain"
}
```

Use a `sites` namespace rather than a catch-all `/v1/{hostname}` to keep site operations distinct from `/v1/projects`, `/v1/jobs`, and future endpoints. The hostname remains the upsert key: create if absent; update if already agent-owned; return a safe conflict if owned by unrelated/manual configuration.

`port` is the host's loopback upstream port, not Caddy's public listen port. `zerodowntime` is a required boolean, not a string. Unknown body fields are rejected. `secondary_port` is valid only when `zerodowntime` is true; if omitted, allocate a different free port from a root-configured range. `template_id` may be omitted only when one default template is configured. `reason` is optional and bounded.

Minimal requests with a configured default template:

```json
{"port": 3001, "zerodowntime": true}
```

```json
{"port": 4001, "zerodowntime": false}
```

Every request uses the main specification's mTLS/HMAC protocol, signed exact request target, actor identity, idempotency contract, and new `sites.write` scope. The backend must authorize the actor for the hostname and associated project. This scope explicitly grants template-based provisioning; `deploy.execute` is still required for a subsequent image deployment.

## 2. Upsert behavior

| Existing state | Behavior |
| --- | --- |
| No managed site exists | Reserve the hostname and requested ports; persist a configuration job. Generate a managed project in maintenance mode. |
| Managed site exists, same effective parameters | Return an attributable successful no-op job; do not restart containers or reload Caddy unnecessarily. |
| Managed site exists, ports change | Prepare a new configuration generation. Preserve the current generation until the update is safely applied. |
| Requested hostname exists in unmanaged Caddy configuration | Return `409 SITE_NOT_MANAGED`; do not silently overwrite or adopt it. |
| Requested port is allocated to another managed site or occupied by an unrelated listener/container | Return `409 PORT_IN_USE`; preserve the existing site. |
| Managed site is already being mutated | Return `409 PROJECT_BUSY` for site configuration changes; retry after the job completes. Ordinary deployment jobs retain their FIFO behavior. |
| Existing site changes deployment mode while running | Return `409 MODE_CHANGE_REQUIRES_STOP`; perform explicit confirmed stop before changing topology in v1. |
| Existing site's template changes | Return `409 TEMPLATE_CHANGE_NOT_SUPPORTED`; template migration is a separate local operation in v1. |

One hostname maps to one managed project in v1. Domain renames, aliases, wildcard sites, deleting domains, arbitrary upstream hosts, and template uploads are not implicitly added by this endpoint. A different hostname is a new site, not a rename or overwrite of an old site.

Hostname validation accepts normalized lowercase ASCII DNS names (including validated punycode). Reject raw Unicode, trailing dots, URL schemes, paths, userinfo, whitespace, braces, ports, IP literals, wildcards, and reserved internal/control-plane hostnames. Require a match against root-owned allowed names or suffixes, using DNS label boundaries. Never allow this endpoint to modify its own agent hostname, the Caddy administration listener, TLS trust, or shared/global Caddy directives.

No-op detection happens after authentication, idempotency lookup, validation, and ownership checks. Repeating an identical request with the same idempotency key returns its original response even if the asynchronous job has since finished.

## 3. Port ownership and conflict detection

Allocate only TCP host ports from a configured application range, for example 3000–9999, excluding reserved/control-plane ports. Treat integers outside 1–65535 as invalid and out-of-policy ports as forbidden. Both blue/green ports must be distinct.

1. Under an admission/allocation lock, check durable reservations, existing generated configurations, Docker published ports, and effective host listeners. A wildcard bind on `0.0.0.0` or an overlapping dual-stack bind may conflict with `127.0.0.1`.
2. Persist the job and pending reservations in one transaction. Enforce a unique host-port reservation key, so concurrent site requests cannot both receive the same port.
3. An already reserved port is reusable only for the exact existing binding it belongs to. The current container listening on its own assigned port is not a conflict. Ownership must be proven, not inferred from the hostname or an HTTP response.
4. A new site's listening port cannot be silently adopted from an arbitrary existing service. For Caddy-only operation, an explicit existing-project association would be required instead of generated-project provisioning.
5. Recheck immediately before creating/reconfiguring containers. An external process can acquire a port after admission; handle Docker bind failure safely and retain the existing route. Never stop an unrelated process to free a port.
6. Keep old reservations through route activation and drain. Release them only when old containers/configurations no longer use them, or after positively verified cleanup of a failed candidate. Unresolved recovery retains reservations.

Known admission conflicts return synchronously:

```http
HTTP/1.1 409 Conflict
Content-Type: application/json
```

```json
{
  "error": {
    "code": "PORT_IN_USE",
    "message": "Port 3001 is unavailable for this site.",
    "request_id": "req_example"
  }
}
```

If a conflict appears after the server already returned `202`, the job fails with `PORT_IN_USE`; an asynchronous API cannot retroactively change that HTTP response. A port-availability check is not a system-wide guarantee against unrelated processes racing to bind it.

## 4. Generated runtime modes

### `zerodowntime: true`

Run the one configured app service from the same Compose file under two project identities, such as `myapp-blue` and `myapp-green`. Persist `blue_port`, `green_port`, each project's digest/environment bindings, and the selected template version. Bind both instances to loopback. Each instance must have a Docker health check and a slot-specific readiness endpoint derived from the template. Never use Compose scaling on a service with one fixed host port as a substitute for distinct slot bindings.

Generate separate blue and green Caddy upstream snippets and a live pointer. Caddy sends ordinary new requests to one active slot only; it does not balance traffic across both versions. The main deployment workflow pulls/starts the inactive slot, verifies health, switches the route, drains, and stops the previous slot.

The request's `port` establishes blue's configured port; it does not mean “always make this port active.” A no-op upsert must not switch from a healthy green slot back to blue. `secondary_port` establishes green's port; when omitted on updates, retain the site's existing secondary port. Allocate automatically only when creating a new blue-green pair or when a mode change requires one.

### `zerodowntime: false`

Run the same single-service Compose file under one managed project identity, with one Caddy upstream binding to `port`. This mode explicitly permits downtime during deployments and restarts. Health checks still apply; “false” does not disable readiness verification or safe configuration validation.

For single-slot deploy/restart/rollback/environment application: validate and pull the requested image before taking the current version down, switch to maintenance, drain and stop the old container, recreate the single service with the selected image/environment, verify health, and restore traffic. If the replacement fails, retain maintenance and report failure; optionally restoring a recorded known-good release is a separate audited recovery action. There is no zero-downtime promise or claim that all pre-activation failures leave the old container running once it has intentionally been stopped.

The site status and all single-slot mutation responses include `downtime_expected: true`. The backend must make this visible to operators. The signed site mode is the explicit choice to use single-slot deployment behavior; confirmed stop remains a separate requirement. For single-slot restart, omit `mode`; an explicit `mode: rolling` returns `409 ROLLING_NOT_AVAILABLE` rather than silently causing downtime under a rolling request.

## 5. Creation, updates, and first deployment

Root-owned provisioning policy supplies templates with a fixed container port, health-check command and readiness path, image repository allow-list, environment-key policy, resource limits, stop/drain settings, safe mounts, and generated-root directories. The request does not supply any of these privileged structures. A template such as `web-node` has a version; persist that version so later local template edits do not silently change existing projects.

Add local policy fields for `enabled`, `allowed_hostnames`/`allowed_domain_suffixes`, `reserved_hostnames`, port range/exclusions, site quotas, default template, and template definitions. Server-wide defaults bound per-site resource limits. Empty hostname policy denies creation. Reserve capacity and cap pending site count to prevent unbounded certificate requests or resource allocation.

Generate stable application project and site IDs, per-slot Compose project names, paths, and file basenames internally. One application project in the API may own two runtime Compose projects; keep these identities distinct in metadata. Keep the root policy immutable to the API. Persist dynamic site/project definitions in SQLite and agent-owned configuration generations beneath configured roots. Root-owned templates plus validated durable site metadata replace the original static-project-only allow-list for these generated projects. Existing locally configured projects retain the original workflow and cannot be adopted implicitly.

On first creation, generate a maintenance-routed site and complete its configuration job. No application container starts without an approved digest and ready environment revision. The response must show `application_state: awaiting_release`, not pretend the site is serving an application.

For generated projects only, allow the first signed `deploy` to initialize from maintenance using a staged environment revision and an allowed digest. This replaces the main specification's local-bootstrap-only rule for generated projects. Start/rollback/restart before first release still return `PROJECT_UNINITIALIZED`. Existing manually configured projects use the local bootstrap workflow unless explicitly migrated by an administrator.

For updates, validate and generate all files as a candidate generation before changing active routing. A generation includes site metadata, Compose bindings, upstream snippets, and route observation identity. Persist intent before publishing a generation pointer; keep previous generations for recovery. All Docker and route changes must be attributable to that generation/job.

Port changes for a running blue-green site prepare a new project generation using a completely free pair of ports and the active release's exact image/environment. Reusing a currently bound port in a new generation is rejected with `409 PORT_MIGRATION_REQUIRES_STOP` unless the effective request is a no-op. Start and health-check the new generation's candidate before switching, then drain/stop the old generation and release its ports. This may require extra temporary capacity. Changing only the inactive port can be deferred until stopped in v1 to avoid ambiguous partial topology changes.

Port changes for a running single-slot site require the confirmed stop workflow first, then the site update, then start. Mode changes in either direction also require stopped/maintenance state. The update preserves the recorded active image/environment so start can restore it under the new topology. These limits keep upsert semantics explicit without pretending every topology migration is zero downtime.

## 6. Caddy update transaction

Use a dedicated generated-site include location in the main Caddyfile, installed locally. The API edits only that managed location. Fixed agent/mTLS and unrelated manual sites live outside it. Reserve/validate hostname ownership against both managed metadata and effective Caddy routes; reject overlapping host matchers that make ownership ambiguous.

The main specification's global route lock and durable journal cover site creation and updates as well as deployment switches. Persist candidate files, update the managed generation pointer atomically, validate the full Caddy config, reload without stopping Caddy, read the live config, and commit observed success. Definite failures restore the previous generated pointer and verify the prior route. Ambiguous reloads retain evidence and containers, block conflicting writes, and reconcile live state before cleanup.

For a new hostname, Caddy configuration acceptance and public TLS readiness are different states. Report them separately. A successful reload does not prove the domain resolves to this VPS or that a public certificate has been issued. Track `tls_state: pending|ready|error` with safe errors, bounded verification, and no arbitrary network-probe URLs. DNS ownership authorization is the backend's responsibility; ACME issuance additionally requires the challenge prerequisites. Use staging certificates during integration tests. [Caddy automatic HTTPS](https://caddyserver.com/docs/automatic-https)

## 7. Responses and reads

```http
HTTP/1.1 202 Accepted
```

```json
{
  "job_id": "job_example",
  "status": "queued",
  "action": "site_upsert",
  "site_id": "site_example",
  "project_id": "project_example",
  "hostname": "example.com",
  "operation": "create",
  "zerodowntime": true,
  "ports": {"blue": 3001, "green": 3002}
}
```

`operation` is `create`, `update`, or `noop`. Allocated IDs/ports and the original accepted response are durable before returning. Single-slot responses use `ports: {"single": 4001}` and expose the expected downtime semantics. Do not return filesystem paths, raw Caddy text, credentials, or environment values.

Add `GET /v1/sites` and `GET /v1/sites/{hostname}` under `deploy.read`. Status includes desired/effective configuration generations, project/job IDs, hostname, deployment mode, port assignments, application state, active slot where applicable, Caddy reconciliation, TLS state, and safe warnings. Safe generated configuration metadata is inspectable before the first app release.

Add tables for `sites`, `site_config_generations`, and `port_reservations`, with unique canonical hostname and port constraints, project foreign keys, pending/active/retiring state, operation ownership, and recovery references. Extend job and audit action enums for site upsert and generated-project initialization. Add stable errors `HOSTNAME_INVALID`, `HOSTNAME_NOT_ALLOWED`, `SITE_NOT_MANAGED`, `PORT_INVALID`, `PORT_NOT_ALLOWED`, `PORT_IN_USE`, `PORT_POOL_EXHAUSTED`, `MODE_CHANGE_REQUIRES_STOP`, `PORT_MIGRATION_REQUIRES_STOP`, and `TEMPLATE_CHANGE_NOT_SUPPORTED`.

## 8. Required tests and implementation placement

Build site metadata, reservations, policy validation, and signature/scope handling in foundation work. Implement generated Compose/configuration and maintenance-first creation alongside bootstrap/routing. Implement single-slot behavior and supported topology updates before declaring operations complete.

Required tests:

- First create, existing-site update, and no-op upsert; retries allocate no duplicate sites or ports.
- Foreign/manual hostname collisions and rejection of agent/control-plane hostnames.
- Hostname/Caddy injection attempts and out-of-policy ports/templates.
- Two simultaneous requests racing for the same hostname/port; durable reservations across restart.
- Existing own-port reuse versus unrelated listener, wildcard bind, and Docker-published-port conflicts.
- Automatic secondary-port allocation, pool exhaustion, and explicit duplicate ports.
- Literal boolean validation, false-mode one instance, true-mode two independently healthy slot projects using the same unchanged Compose file and service name.
- First deployment from maintenance and honest awaiting-release/TLS status.
- Port change health failure preserves the previous live site; definite and ambiguous Caddy reload failures follow the recovery journal.
- Deployment/site-upsert interleaving cannot edit the same project concurrently or bypass the global Caddy lock.
- Mode changes require stop; the old release/environment is preserved for start.
- Single-slot deploy failures stay maintenance-routed and are never reported as zero downtime.
- Public DNS/TLS readiness is not confused with successful Caddy reload.
- Crash recovery of candidate generations, pending reservations, and retired-port cleanup.

These additions expand v1 scope and replace the source's prohibition on remote creation only for validated template-based managed sites. They do not introduce a general Caddy or Docker administration API.
