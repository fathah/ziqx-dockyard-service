import type { Inventory, Job, Project, Release } from "./types";
const date = "2026-09-29T08:30:00Z";
const release = (n: number): Release => ({
  id: `rel-preview-${n}`,
  image: `ghcr.io/ziqx/preview@sha256:${String(n).repeat(64).slice(0, 64)}`,
  environment_revision: `env-preview-${n}`,
  compose_revision: `compose-preview-${n}`,
  created_at: date,
});
export const demoProjects: Project[] = [
  ["accounts", "production", "running", false, 4100, "blue"],
  ["accounts", "staging", "running", false, 4102, "blue"],
  ["accounts", "development", "stopped", false, 4103, "blue"],
  ["payments", "production", "running", true, 4200, "green"],
  ["search", "production", "running", true, 4300, "blue"],
  ["notifications", "staging", "running", false, 4400, "blue"],
].map(([app, env, state, zero, port, active], i): Project => ({
  id: `${app}-${env}`,
  app_id: String(app),
  environment: env as Project["environment"],
  template_id: i === 0 ? "" : "web-node",
  ...(i === 0
    ? { mode: "compose" as const, route_service: "web", route_port: 80 }
    : {}),
  domains: [`${env === "production" ? "" : env + "."}${app}.example.com`],
  zerodowntime: Boolean(zero),
  blue_port: Number(port),
  green_port: zero ? Number(port) + 1 : undefined,
  active_slot: String(active),
  state: String(state),
  slots: zero
    ? { blue: release(i + 1), green: release(i + 2) }
    : { blue: release(i + 1) },
  releases: [release(i + 1), release(i + 2)],
}));
export const demoInventory: Inventory = {
  last_sync_at: date,
  projects_observed_at: date,
  caddy_observed_at: date,
  docker_observed_at: date,
  projects: [
    {
      id: "existing-example",
      name: "legacy-api",
      environment: "production",
      managed: false,
      present: true,
      compose_files: ["compose.yaml"],
      services: [
        {
          name: "app",
          image: "ghcr.io/ziqx/legacy:latest",
          published_ports: [4500],
        },
        { name: "redis", image: "redis:7", published_ports: [] },
      ],
      warnings: [],
    },
  ],
  sites: [
    {
      host_matcher: "legacy.example.com",
      upstreams: ["127.0.0.1:4500"],
      project_ids: ["existing-example"],
    },
  ],
  reserved_ports: [4500],
  warnings: [],
};
export const demoJobs: Job[] = [
  {
    job_id: "job-preview-01",
    project_id: "payments-production",
    action: "deploy",
    status: "succeeded",
    phase: "complete",
    actor_id: "mac-owner",
    request_id: "req-preview-01",
    created_at: date,
    finished_at: date,
  },
  {
    job_id: "job-preview-02",
    project_id: "accounts-staging",
    action: "restart",
    status: "succeeded",
    phase: "complete",
    actor_id: "mac-owner",
    request_id: "req-preview-02",
    created_at: date,
    finished_at: date,
  },
];
