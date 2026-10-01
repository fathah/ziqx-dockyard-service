# Compose deployments

New Dockyard projects use **Docker Compose YAML and `.env` contents**. No templates, required service names, image repository allowlists, or mandatory image digests. Docker's installed Compose version validates and resolves the configuration.

## Desktop

1. Update the VPS service using **Server details → Server software**.
2. For an existing enrollment, select **Required server access → Compose management → Enable with Touch ID** once. New installations enable management during setup.
3. Create a project and choose a flavor: development, staging, or production. Configure its domains and web service if using Caddy. Deploy the complete stack first; update individual services from Services afterward.
4. Once creation succeeds, open the project and choose **Deploy Compose**. Import or paste Compose YAML and `.env` in **Configuration**, then choose **Deploy all services**. New projects open an empty editor without fetching a nonexistent release. The native `.env` picker supports hidden files with Command–Shift–period.
5. For another flavor, choose **Add flavor** on the project, select an unused environment, and give it its own domains and files. This preserves the same application identity. Existing flavors cannot be created twice.

Configuration always submits both files; an empty `.env` clears saved values. The older Deploy Compose dialog leaves saved values unchanged when its environment field is blank; import an empty file or enter `# empty` there to clear them.

Dockyard assigns its own Compose project identity and ownership labels; supplied top-level `name` and `COMPOSE_PROJECT_NAME` do not override them. All services in the supplied Compose file are deployed, including services behind `profiles`; Dockyard explicitly enables every profile during resolution. Supply a separate Compose file per flavor when the service set should differ.

Domains are optional. To use Caddy, provide domains, the web service name, and its container port when creating the project. Dockyard assigns a loopback host port for that service. With no domains, your Compose ports are used as supplied. Caddy routes and allocated ports remain subject to ownership/conflict checks.

Each flavor has a separate directory, such as `/docker/shop-development`, `/docker/shop-staging`, and `/docker/shop-production`, containing its own `compose.yml` and `.env`. Caddy web ports are allocated separately. Any additional explicit host ports, fixed container names, external networks, and bind paths in your files must also be distinct where isolation is needed; Dockyard does not rewrite arbitrary shared resources.

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

## Individual service updates

Open **Services → Pull & update** beside a service. Review its update strategy, then approve the selected service. This requires the desktop and VPS service 0.7.0 or later.

**Seamless updates** are available for the production service serving the project's domains. Dockyard pulls only its image, skips deployment when the image ID is unchanged, starts a replacement on another loopback port, checks its Compose healthcheck and optional HTTP path, switches Caddy, drains requests, and stops only the previous application container. Databases and other dependencies continue running in the same Compose project and on the same networks. There is no need to split your database into another project or another supplied Compose file.

The app must safely support two running versions. Writable persistent mounts, image-declared volumes, extra published ports, fixed IPs, shared runtime namespaces require a controlled restart. Individual service updates currently support one container per service; services with multiple replicas use the full-stack deployment workflow. A fixed `container_name` is removed only from the generated app replacement. Database names, volumes, networks and the full supplied Compose file are retained. Readiness checks and a traffic switch alone cannot guarantee uninterrupted connections: the app must handle graceful shutdown, compatible database migrations and the configured drain interval.

**Controlled restart** pulls and recreates only the selected service with `--no-deps`. Other services and data volumes remain in place. Use it for databases, background workers, development and staging. Locally built services use **Edit & deploy** instead; the button pulls registry images, not a build context. Database image upgrades still require the operator's usual backup and version-compatibility process.

Service updates use the saved Compose and `.env`. Generated immutable manifests and image IDs, reusable app ports, active service versions and recovery plans are tracked privately in SQLite and the project folder. The two app update ports alternate across updates. Logs follow the active service version. **Start** and **Restart** preserve those selected versions. **Deploy all services** and full-stack rollback are explicit maintenance operations and replace individual overrides with the full saved configuration. They do not roll back volume contents, database migrations or external files.

For adopted projects, the first seamless update reviews the existing plain `reverse_proxy` Caddy sites and imports those routes into Dockyard while preserving the existing Compose identity. Custom handlers need manual route review. Existing projects using the older whole-stack update strategy retain that behavior; the desktop does not automatically convert them.

If readiness fails before switching, the existing app remains live and the failed replacement is stopped. If a traffic switch has an uncertain outcome, both versions are preserved and writes are blocked until local reconciliation verifies which route is live. After a completed switch, an unhealthy replacement is routed back to the previous app before cleanup when the old route can be verified. Controlled-restart failures also require reconciliation of the selected image and running container.

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

Existing template-managed projects keep their previous behavior; see [legacy Compose policy](LEGACY_COMPOSE.md). Existing VPS inventory can be adopted conservatively in place. Migration assessment does not require templates or pinned images.
