# Existing-project migration

After a successful deployment, Dockyard writes operator copies of `compose.yml`
and `.env` under `/docker/<managed-project-id>`. Imported projects retain their
original Compose working directory for build contexts and relative files, but
deployment uses verified release snapshots in the managed folder. Original
Compose files under `/docker/<source-name>` are preserved. Check the managed
folder when verifying edits; `COMPOSE_MIRROR_FAILED` or `ENVIRONMENT_MIRROR_FAILED`
means a copy could not be written after activation, while containers use the
immutable release files.

The desktop configuration editor reads the saved release snapshot. Editing a
VPS Compose file or running `docker compose up -d` outside Dockyard does not
import those edits or update Dockyard's recorded deployment state. A failed
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
changes. An imported project whose recovery reports
`COMPOSE_RECOVERY_REQUIRES_INSPECTION` still needs inspection of its live Compose
identity and original Caddy route before any route changes.

Service 0.4.0 supports in-place adoption of existing Compose projects. It does not need a replacement stack or a traffic switch. Production projects with databases, named volumes, bind mounts and existing published ports can keep those resources.

1. `GET /v1/inventory/{id}/migration` inspects live containers and resolves Docker Compose configuration. It verifies one project name, working directory and consistent source files; detects a shared project identity or unmatched services; and returns a review hash. Secrets remain on the VPS.
2. The desktop shows the plan and requests Touch ID. `POST /v1/inventory/{id}/migrate` requires the review hash, `projects.write`, `deploy.environment`, server-wide project access and the credential's `compose.admin` capability.
3. The server rechecks the plan, saves private immutable Compose/environment snapshots under `/docker/<inventory-id>`, and atomically accepts the project and metadata job into SQLite. Exact original container IDs and Compose labels establish ownership before those containers gain Dockyard labels on a later deployment. The job does not start, stop, replace or relabel containers, write original source files, or alter Caddy.
4. The project becomes managed. Deploy, rollback, start, restart, stop and redacted logs use the original Compose project identity. Resolved volume and network names, published ports and working directory are retained. Metadata-only adoption jobs safely resume after daemon interruption.

Adopted stacks use a single deployment instance. Future deployments/restarts may interrupt service, and rolling back configuration does not undo database changes. Caddy routes remain in the original Caddyfile and are displayed as preserved routes; editing them through generated project snippets is disabled. This is not blue-green conversion or a database backup.

The review rejects missing containers, shared/ambiguous Compose identities, source symlinks or paths outside the project folder, unreadable/oversized files, and mismatched Compose services. It supports up to 16 source files when the existing container labels identify the files. Active profiles must match the source's configured defaults. Source folder names currently follow Dockyard IDs (lowercase letters, digits, hyphens; start with a letter; at most 48 characters). A source change after review requires a fresh review. Original deployment files remain available; use Dockyard for subsequent deployments to avoid external configuration drift.
