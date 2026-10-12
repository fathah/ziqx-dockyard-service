# Existing-project migration

From VPS service **0.7.28**, imported projects use one application folder:
`/docker/<source-name>`. Private revisions, saved drafts, slot bindings and the
deployed Compose mirror live under its `.dockyard` subdirectory. The original
Compose files, build contexts, data and working directory stay in place; the
active `.env` mirror is written in the application folder after deployment.
On startup, Dockyard moves the previous `/docker/existing-<hash>` metadata
directory into the original folder's `.dockyard` and regenerates absolute slot
bindings. Project IDs, job history, container identities, mounts and routes are
retained. A conflicting destination is never overwritten. New migrations do
not create a second top-level directory. `COMPOSE_MIRROR_FAILED` or `ENVIRONMENT_MIRROR_FAILED`
means a copy could not be written after activation, while containers use the
immutable release files.

The desktop configuration editor reads the saved release snapshot. Editing a
VPS Compose file or running `docker compose up -d` outside Dockyard does not
import those edits or update Dockyard's recorded deployment state. From 0.7.29,
an import saves the Compose model with `${VAR}` and `env_file` references intact,
alongside the folder's `.env`, after checking that resolving them reproduces the
reviewed configuration exactly; otherwise resolved values are frozen as before.
Editing `.env` then applies on Deploy, Start/Restart and service updates. Environment
updates resolve the original Compose environment rules against the latest saved
`.env`, including older (or frozen) adoption baselines with resolved inline values.
Only environment changes are merged into service updates and Start/Restart;
other Compose changes require a configuration deployment. From 0.7.29, the configuration
editor (including a saved draft) shows the original `environment`/`env_file`
rules, for example `${VAR}`, for every service whose environment still equals
the frozen adoption values, even after earlier redeployments of that text, so a
`.env`-only edit applies on Deploy. Other settings stay as last deployed. A failed
deployment does not activate the candidate or replace the operator copies.

With VPS service 0.7.4+, open the project's **Domains → Configure route** to
review an existing Compose service and its published port. An imported project
with detected Caddy domains includes the complete detected site group; only
plain local reverse-proxy sites can be imported automatically. The review is
bound to the saved release, upstream and Caddyfile. Applying it with Touch ID
moves the reviewed site into Dockyard's generated routes without recreating
containers, changing published ports or editing Compose files. Projects with no
detected domains can create a route for allowed hostnames instead. DNS records
are configured separately. Subsequent route edits keep the existing port fixed;
deploy a reviewed Compose change to move it. Changed bindings block live health
verification. A later seamless app update replaces this binding with its
reviewed managed app port.

Route setup requires a verified running release and no active or recovery job.
It does not clear an interrupted operation or import manual VPS configuration
changes. Version 0.7.5 supports offline lifecycle recovery for single-instance
Compose projects without a managed domain route. It verifies all containers in
the Compose namespace, including orphans. Original adopted IDs identify the
adoption snapshot; managed containers must uniquely match a pinned release's
service configuration hashes and pass health/count checks. A stopped or absent
verified stack is recorded as stopped. Recovery does not change containers,
operator Compose files or external Caddy routes. Recreated unlabelled containers,
unknown configurations, ambiguous releases and changing state retain the lock.
Older servers return `COMPOSE_RECOVERY_REQUIRES_INSPECTION`; update the server
before running reconciliation again. Manual VPS Compose changes are not imported.

Service 0.4.0 supports in-place adoption of existing Compose projects. It does not need a replacement stack or a traffic switch. Production projects with databases, named volumes, bind mounts and existing published ports can keep those resources.

1. `GET /v1/inventory/{id}/migration` inspects live containers and resolves Docker Compose configuration. It verifies one project name, working directory and consistent source files; detects a shared project identity or unmatched services; and returns a review hash. Secrets remain on the VPS.
2. The desktop shows the plan and requests Touch ID. `POST /v1/inventory/{id}/migrate` requires the review hash, `projects.write`, `deploy.environment`, server-wide project access and the credential's `compose.admin` capability.
3. The server rechecks the plan, saves private immutable Compose/environment snapshots under `/docker/<source-name>/.dockyard`, and atomically accepts the project and metadata job into SQLite. Exact original container IDs and Compose labels establish ownership before those containers gain Dockyard labels on a later deployment. The job does not start, stop, replace or relabel containers, write original source files, or alter Caddy.
4. The project becomes managed. Deploy, rollback, start, restart, stop and redacted logs use the original Compose project identity. Resolved volume and network names, published ports and working directory are retained. Metadata-only adoption jobs safely resume after daemon interruption.

Adopted stacks use a single deployment instance. Future deployments/restarts may interrupt service, and rolling back configuration does not undo database changes. Caddy routes remain in the original Caddyfile and are displayed as preserved routes; editing them through generated project snippets is disabled. This is not blue-green conversion or a database backup.

The review rejects missing containers, shared/ambiguous Compose identities, source symlinks or paths outside the project folder, unreadable/oversized files, and mismatched Compose services. It supports up to 16 source files when the existing container labels identify the files. Active profiles must match the source's configured defaults. Source folder names currently follow Dockyard IDs (lowercase letters, digits, hyphens; start with a letter; at most 48 characters). A source change after review requires a fresh review. Original deployment files remain available; use Dockyard for subsequent deployments to avoid external configuration drift.
