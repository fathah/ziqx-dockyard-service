# Migrating an existing Compose project

An observed project is **not** a managed project. The inventory importer records
safe metadata in SQLite, but it does not own the existing Compose project,
containers, environment, volumes, or Caddy route. Changing `managed` in the
inventory record would not grant that ownership: Dockyard's managed containers
use generated Compose project names, immutable revisions, ownership labels,
approved root templates, and verified Caddy snippets.

## First supported migration path

The first `Migrate Now` implementation should support only a stateless stack
whose source can be validated against Dockyard's existing Compose policy. It
must be an explicit, durable migration job, not an inventory flag change.
The source project keeps serving until its replacement is healthy and the route
has been switched. Its files and containers remain available for rollback until
the operator explicitly retires them. Never run `docker compose down -v` or
prune during migration.

The read-only `GET /v1/inventory/{id}/migration` preflight returns fixed
check codes and a Compose source hash, without returning Compose contents or
secret values. Its current conservative checks cover:

1. A fresh, complete inventory observation; exactly one trusted Compose source;
   no unresolved interpolation, overrides, symlinks, or unsafe ownership.
2. One to eight services that pass the strict submitted-Compose parser, with
   `app` as the HTTP target, digest-pinned images, and a root-approved template
   for every service. The configured template health commands, UID/GID,
   resource limits, and environment allowlists must fit those images.
3. No host bind mounts, external or named persistent volumes, host namespaces,
   devices, privileged settings, custom container names, or other unsupported
   Compose fields. Stateful stacks require a separate data migration design.
4. No known manual Caddy route or existing published host port. These stay
   blocked until the traffic-cutover job can transfer them safely. A missing
   association is not proof that a route is unowned.

The execution preflight must add available replacement ports and resources,
live Docker identity/health, and a fingerprint of all relevant Compose files,
Caddy routes, container IDs, and policy. A changed fingerprint must invalidate
the review. The current source hash alone is not authority to execute.

The desktop shows `Migrate Now` on each observed project. Its current first
screen displays the preflight result and concrete blockers, and clearly says
execution is unavailable. For an eligible project, the future execution flow
will let the operator supply any environment values
through the existing secret handling path and reviews service/template mapping,
new ports, domain ownership, traffic switch, and rollback plan. No secret is
copied from an existing `.env` file into SQLite or returned by the API.

The server then needs a resumable job with these phases:

1. Recheck the fingerprint under the admission lock and reserve destination
   identity, domain, and ports transactionally. Write a private receipt and
   backup of the exact files/routes to be changed.
2. Create a *new* managed Compose identity and immutable revisions. Pull and
   start the replacement alongside the old stack. Verify every container,
   image, mount, ownership label, and health contract.
3. Convert only the reviewed Caddy route, validate disk and live Caddy
   configuration, and reload. Probe the public endpoint before recording the
   traffic switch. If the outcome is uncertain, stop and require recovery;
   never guess that a reload succeeded.
4. Keep the source running through a drain window. Record the new managed
   project in SQLite only with its verified release and route state. Preserve
   the old Compose directory and containers as a rollback target. Retirement
   is a separate, explicit operation after backup and traffic checks.

For this path, production can gain Dockyard's blue-green deployment mode only
after the replacement is under its managed identity and proven stateless.
Migration itself is a traffic cutover; it must not claim zero downtime until
live acceptance tests demonstrate it. Development and staging keep one slot.

## Current installation and scope

The desktop's default first-time VPS setup installs an **inventory-only** root
policy and a read-only API key. Before a migration job can run, the VPS operator
must configure approved templates, domain allowlists, resource budgets, and a
write-capable desktop key. The setup wizard cannot safely infer these from
existing Compose files. The current backend and desktop implement only the
read-only preflight. They do not implement the migration job; observed projects
therefore stay read-only.

Stacks with databases, persistent volumes, manual Caddy directives that cannot
be isolated, or unsupported Compose syntax remain blocked in the first release.
They need a separate reviewed migration path that preserves data and route
behavior. The UI should state the blocker and retain normal inventory access.
