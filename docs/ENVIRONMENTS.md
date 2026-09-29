# Deployment environments

Before creating or deploying a Compose stack, choose **development**, **staging**, or **production**. This choice is separate from the stack's `.env` variables and environment revision.

| Environment | Deployment behavior |
| --- | --- |
| `development` | One Compose instance; replacement may cause downtime |
| `staging` | One Compose instance; replacement may cause downtime |
| `production` | Blue-green by default; health-gated traffic switch and drain |

Only production allows `zerodowntime: true`. Production can explicitly choose `zerodowntime: false` for stacks with persistent named volumes; the existing policy refuses persistent-volume stacks in blue-green mode. Stateless production deployments retain the previous serving slot until the candidate passes checks and the route switches. Live VPS traffic acceptance is still required before claiming zero downtime.

## One app, three independent deployments

Use the same `app_id` and a different project `id` for each environment. SQLite enforces one project per `(app_id, environment)`. Both values are immutable after creation. Each project has its own domains, reserved ports, Docker Compose project names/networks/volumes, secrets, releases, logs and job history under `/docker/<id>`.

For example, `app_id: shop` can have `shop-development`, `shop-staging` and `shop-production`. Root key policy grants access to each project ID explicitly; sharing `app_id` does not grant cross-environment access. List authorized projects and match `app_id` plus `environment` to resolve an existing deployment target.

Create staging:

```json
{
  "id": "shop-staging",
  "app_id": "shop",
  "environment": "staging",
  "template_id": "web-node",
  "domains": ["staging.shop.example.com"]
}
```

Creation defaults `zerodowntime` to false here, allocates one port and installs maintenance. Use a distinct domain and ID for development and production; production defaults to two slots/ports. Explicit secondary ports are rejected for single-instance deployments. Wait for creation to succeed before submitting Compose.

Deploy to `/v1/projects/shop-staging/deploy`:

```json
{
  "environment": "staging",
  "compose_yaml": "services:\n  app:\n    image: ghcr.io/your-org/your-app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n",
  "variables": {
    "DATABASE_URL": "provide-your-staging-secret-value",
    "APP_URL": "https://staging.shop.example.com"
  }
}
```

The create API requires `app_id` and `environment`; every new Compose deploy requires `environment` to match the project. Missing choice returns `422 DEPLOYMENT_ENVIRONMENT_REQUIRED`; another valid environment returns `409 DEPLOYMENT_ENVIRONMENT_MISMATCH` before YAML processing or secret writes. Blue-green outside production returns `400 BLUE_GREEN_PRODUCTION_ONLY`. A second project for the same app/environment returns `409 APP_ENVIRONMENT_EXISTS`.

Clients should ask “Which environment should receive this deployment?” with the three choices before signing the request. `dockyardctl` asks this question when a create/deploy JSON body omits `environment`, with no default selection. Unattended calls can supply `-environment staging` or include the choice in the body. A flag that conflicts with the body is rejected. The selected choice must still match the project addressed by `-path`.

When the CLI adds a choice, it leaves the original body file untouched. Retries must reuse the same body file, explicit `-environment` value, actor/scopes, request ID and idempotency key. Complete bodies already containing `environment` preserve their exact signed bytes and require no prompt. The read-only `/docs` viewer describes this contract; the Super Admin deployment UI remains a separate integration.

## Upgrade behavior

Schema version 2 adds a unique application/environment index. Existing deployments are classified as `production` with `app_id` equal to their existing project ID. Their IDs, directories, ports, single-slot/blue-green mode, releases and running containers remain unchanged. Queued/recovery job project snapshots are migrated too, preserving retry fingerprints and history. No deployments are automatically promoted, cloned or renamed. New deploy requests for these projects must explicitly select production.
