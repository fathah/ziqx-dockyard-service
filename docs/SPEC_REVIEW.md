# VPS deployment agent: specification review

Reviewed: 2026-09-17  
Source: `/Users/fathah/Downloads/VPS_Deployment_Agent_Implementation_Spec.md`, version 1.0  
Scope: design review and specification refinement; no implementation or infrastructure changes.

The project is feasible with the proposed Go, Docker Compose, Caddy, SQLite, and systemd architecture. Keep the per-VPS agent small and keep human authentication, RBAC, and UI in the existing Super Admin system. The difficult work is coordinating durable state with Docker and Caddy when operations fail or the agent crashes.

The user additionally requested API-driven Caddy hostname upserts, port-conflict errors, and a `zerodowntime` flag. That is an intentional scope expansion beyond the source document's preconfigured-project-only model. The route API must manage generated, agent-owned site configuration through typed parameters and the same global route lock; it must not accept raw Caddy configuration. The exact provisioning contract is specified in [CADDY_SITE_API.md](CADDY_SITE_API.md).

The user also asked whether a single Compose setup needs changing for blue-green. The refined proposal uses one parameterized file with one app service, launched under two Compose project names. Only slot bindings and the Caddy route change during ordinary deployments; fixed ports/container names may require one-time preparation of an existing file. This deliberately replaces the original two-service Compose example. `zerodowntime: false` uses one instance and explicitly permits deployment downtime.

Version 1.0 is a strong starting point, but its “Ready for implementation” label is premature. The issues below affect correctness, recovery, or API compatibility. Proposed resolutions are incorporated into [IMPLEMENTATION_SPEC.md](IMPLEMENTATION_SPEC.md). They are design recommendations for this project, not claims that software has been built or validated.

## Findings and resolutions

| Priority | Source sections | Finding | Proposed resolution |
| --- | --- | --- | --- |
| Blocking | 10, 12, 13 | Project locks do not serialize changes to the shared Caddyfile. One project's reload can load another project's tentative symlink, including a change about to be rolled back. | Hold one host-wide route lock across prepare, link replacement, validation, reload, verification, and compensation. Keep image pulls and health checks concurrent. |
| Blocking | 10, 13, 21 | A symlink records intended configuration, not necessarily Caddy's running configuration. A reload timeout can occur after Caddy applied the change. Restoring a link alone does not prove traffic moved back. | Persist switch intent before side effects; inspect the live Caddy configuration through its protected local admin endpoint. Preserve both containers whenever the result is unknown. |
| Blocking | 8, 9, 13, 16 | Both slots use the same mutable `runtime.env`. Candidate preparation replaces that file before candidate health is known. Existing container environments do not change immediately, but later recreation can bind an old image to the candidate's environment. | Give each slot its own environment binding backed by immutable revisions. Persist each slot's image and environment revision together. Never modify the active slot's binding while preparing the candidate. |
| Blocking | 8, 13, 15, 21 | New installations have no active release, yet deployment requires one. Stop needs a maintenance route, but startup accepts only blue/green symlink targets. | Model `uninitialized`, `running`, and `stopped` explicitly. Add a configured maintenance target and a local bootstrap command. Ordinary deploy still rejects stopped projects. |
| Blocking | 6.3, 19 | Signing excludes query parameters, has no key-ID header despite key rotation, and leaves the scope transport unspecified. | Define a versioned signing contract covering target server, key ID, scopes, exact path and query, actor, and body. Enforce a route-to-scope mapping locally. |
| Blocking | 6.3, 11, 12, 18.5 | The request hash is undefined; including timestamp/signature would break retries. Queued work has only redacted payloads, and environment creation has incompatible synchronous/asynchronous response shapes. | Separate replay identity from signature freshness. Persist resolved non-secret execution inputs and durable environment files before acceptance. Return a stable job envelope for every mutation. |
| High | 11, 21, 22 | No durable route intent, previous/candidate binding, phase events, or drain deadline exists in the proposed schema. Metadata alone cannot classify interrupted work. | Add slot bindings, route-switch records, job phase events, and recovery-required outcomes. Recover from observed runtime evidence; block ambiguous mutations. |
| High | 9, 13, 20 | `compose up --wait` can accept a running service without a health check; a rolling restart may reuse an existing candidate when its configuration is unchanged. | Require an effective Docker health check, independently verify healthy status and image identity, and force recreation of only the candidate. |
| High | 8, 9, 16, 20 | Generic dotenv encoding can expand `$` expressions. Compose project names and both image variables are not durably specified. Bootstrap tag defaults violate the immutable-image intent. | Specify literal, round-trip-tested dotenv encoding; explicit Compose project names; complete slot bindings; and required digest variables without mutable tag fallbacks. |
| High | 11.5, 12, 25 | SQLite and JSONL cannot be committed atomically together. Per-project mutexes alone do not guarantee FIFO scheduling or fair global concurrency. | Use a transactional audit outbox and durable FIFO job order per project. Schedule eligible projects fairly without consuming worker slots while waiting for project locks. |
| High | 16, 17, 26 | Absolute secrecy claims conflict with returning application logs, which may already contain secrets. | Keep stored environment values out of agent-generated responses, diagnostics, metrics, and audit. Redact known values in application log output, while documenting the limit for transformed or unrelated secrets. |
| High | 6.4, 23, 29 | Certificate revocation is a runbook heading, not an implemented mechanism. A CA trust file alone does not define a revocation policy. | Require a tested emergency trust/credential replacement procedure; identify whether a verifier module is used before promising individual certificate revocation. |

## Technical checks

- Caddy supports reading the active configuration and applies configuration replacement without stopping the process. This supports live-state verification, but does not make a multi-step filesystem/database/HTTP operation atomic. The host-wide lock and switch journal are design conclusions from that limitation. [Caddy API](https://caddyserver.com/docs/api)
- `caddy reload` sends configuration to the running server. A command result and a file on disk are different observations. [Caddy command line](https://caddyserver.com/docs/command-line#caddy-reload)
- Compose documents `--wait` as waiting for services to be running or healthy. Therefore, it is insufficient as the only health gate when a service has no health check. [Compose up](https://docs.docker.com/reference/cli/docker/compose/up/)
- Compose expands unquoted and double-quoted environment values. Single-quoted values are literal; encoder behavior still needs tests using real Compose. [Compose interpolation](https://docs.docker.com/compose/how-tos/environment-variables/variable-interpolation/)
- Compose supports `env_file.format: raw` starting with 2.30.0, but choosing it would require an explicit version floor and a value-format contract. The revision keeps a tested dotenv encoder to preserve multiline-value support. [Compose service environment files](https://docs.docker.com/reference/compose-file/services/#env_file)
- The example `require_and_verify` plus `trust_pool file` matches the current documented Caddy syntax. Revocation checking is described under custom certificate verifiers, so CA trust alone should not be presented as a complete revocation solution. [Caddy TLS](https://caddyserver.com/docs/caddyfile/directives/tls)

## Build sequence

1. **Foundation:** strict configuration, durable schema, signature test vectors, scope enforcement, idempotency, audit outbox, bounded process runner, and read APIs. Exit when malformed/unauthorized requests cause no side effects and duplicate requests cannot create duplicate jobs.
2. **Deployment:** local bootstrap, slot bindings, health gates, globally serialized route switch, persistent drain, and crash recovery. Exit when fault-injection tests preserve the serving release and uncertain route outcomes preserve both containers.
3. **Operations:** rollback, rolling restart, confirmed stop/start, environment staging/application, bounded logs, and safe retention. Exit when rollback restores the exact retained image/environment pair and cleanup cannot remove referenced state.
4. **Production validation:** Linux integration environment, real mTLS, crash tests, 100 alternating deployments under traffic, packaging, metrics, and operational runbooks. Exit only with recorded staging evidence for the acceptance checklist.

The first useful end-to-end milestone is one configured application bootstrapped locally, then deployed blue-to-green and green-to-blue through the signed API while ordinary traffic remains successful. Add the Super Admin screens after that works.

## Local starting point and remaining inputs

The project directory was empty when reviewed. Go and the Docker CLI are installed; Caddy was not found on the current PATH. Docker daemon availability and a Linux staging host were not tested. No implementation tests were run because this change only creates documents.

Before staging, supply the VPS distribution and architecture, the installed Docker/Compose/Caddy versions to pin, application and agent hostnames, the GHCR repository, the application's readiness contract, required environment keys, and the Super Admin integration location. These details do not block foundation development. Confirm sufficient capacity to run two application versions concurrently and confirm the application supports overlapping versions against its shared database.
