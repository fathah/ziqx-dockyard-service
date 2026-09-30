# Existing-project migration

Existing VPS inventory records observations, not ownership of containers or traffic. New managed projects use Compose and `.env` without templates. Migration assessment no longer requires approved templates, a service named `app`, or image digests.

The current `/v1/inventory/{id}/migration` endpoint is read-only. It checks source presence/trust, complete recent inventory, project naming, a single Compose source, and whether existing Caddy routes/published ports need a traffic cutover. Passing these checks does not execute migration.

A future takeover job must capture the exact Compose identity, files, environment, volumes, container IDs and routes; prepare a reviewed plan; preserve data and rollback files; verify live ownership; and persist its progress. Moving traffic requires a healthy replacement and a verified Caddy switch. Persistent stacks need a data-preserving adoption or migration path. It must never delete volumes or merely flip an inventory flag.

Until that job is implemented, existing observed projects remain read-only. Updating the server removes obsolete template blockers but does not grant automatic ownership of running services. New managed projects can already use the [Compose deployment flow](COMPOSE.md).
