# Compose deployments

New Dockyard projects use **Docker Compose YAML and `.env` contents**. No templates, required service names, image repository allowlists, or mandatory image digests. Docker's installed Compose version validates and resolves the configuration.

## Desktop

1. Update the VPS service using **Server details → Server software**.
2. For an existing enrollment, select **Required server access → Compose management → Enable with Touch ID** once. New installations enable management during setup.
3. Create a project and choose development, staging, or production.
4. Deploy the Compose YAML and paste the `.env` contents. Leave the desktop field blank to reuse saved values on redeploy; a new project starts with no values. To clear saved values, submit a comment-only file such as `# empty`.

Dockyard assigns its own Compose project identity and ownership labels; supplied top-level `name` and `COMPOSE_PROJECT_NAME` do not override them.

Domains are optional. To use Caddy, provide domains, the web service name, and its container port when creating the project. Dockyard assigns a loopback host port for that service. With no domains, your Compose ports are used as supplied. Caddy routes and allocated ports remain subject to ownership/conflict checks.

```yaml
services:
  web:
    image: nginx:${NGINX_TAG}
    ports:
      - "8080:80"
  database:
    image: postgres:17
    env_file: .env
    volumes:
      - database:/var/lib/postgresql/data
volumes:
  database: {}
```

```dotenv
NGINX_TAG=alpine
POSTGRES_PASSWORD=replace-with-your-secret
```

Build contexts, bind sources, includes, configs, and additional secret/env files must already exist on the VPS. Relative paths resolve from `/docker/<project-id>`. Dockyard uploads the Compose and `.env` inputs; it does not upload a source tree. Use Docker healthchecks for application readiness. Without them, Compose waits for services to run, which does not prove application readiness.

A successful deployment mirrors the submitted source into `/docker/<project-id>/compose.yml` and `.env`. Docker runs private, verified revisions. Normal API reads expose safe service metadata from SQLite; environment values and resolved source are write-only.

## Authorization

Full Compose can mount the Docker socket, host directories, or run privileged services. It grants **root-equivalent control of the VPS**. Only a server-wide credential explicitly granted `compose.admin` can create or mutate Compose projects. Existing action scopes, mTLS, HMAC signatures, replay protection, and desktop Touch ID still apply. Project-scoped credentials cannot gain this capability. Template-mode policy remains enforced for older projects.

Fresh desktop setup grants this access. For existing servers, the desktop's Touch ID action validates the updated configuration, saves a private backup, and restarts Dockyard. If startup fails it restores the previous configuration. Containers and Caddy are not restarted by that action.

## Environments and blue-green

Single-instance deployment is the default in every environment. It supports ordinary Compose features, including databases and persistent volumes. Updating that stack can cause downtime. Docker storage is retained; Dockyard does not run `down --volumes`.

Production can opt into blue-green with a Caddy route. Both copies must run independently: no shared volumes, external/named shared networks, fixed container names, extra host ports, or host namespace sharing. Every service needs a healthcheck. If incompatible, use a single instance. Image-declared persistent mounts also prevent a blue-green traffic switch. Compose validity alone cannot guarantee zero downtime.

Rollback reuses the saved Compose/environment configuration. It does not roll back volume contents, database migrations, external files, or mutable image tags. Pin a digest if exact image repeatability is needed.

## API

Create without `template_id`:

```json
{"id":"demo-production","app_id":"demo","environment":"production","domains":[]}
```

Then submit to `/v1/projects/demo-production/deploy`:

```json
{"environment":"production","compose_yaml":"services:\n  web:\n    image: nginx:${TAG}\n    ports: [\"8080:80\"]\n","env_file":"TAG=alpine\n"}
```

`env_file` contains raw file contents. Omitting it reuses the current snapshot; `""` supplies an empty `.env`. Input limits are 64 KiB each, within the HTTP body limit. Refer to `/docs` for signed headers and job polling.

Existing template-managed projects keep their previous behavior; see [legacy Compose policy](LEGACY_COMPOSE.md). Existing VPS inventory still needs a separate takeover/traffic migration implementation before Dockyard can manage it in place. Migration assessment no longer requires templates or pinned images.
