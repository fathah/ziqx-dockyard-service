export type Environment = "development" | "staging" | "production";
export type Release = {
  id: string;
  image: string;
  environment_revision: string;
  compose_revision?: string;
  created_at: string;
};
export type Project = {
  mode?: "compose";
  route_service?: string;
  route_port?: number;
  readiness_path?: string;
  id: string;
  app_id: string;
  environment: Environment;
  template_id: string;
  domains: string[];
  zerodowntime: boolean;
  blue_port: number;
  green_port?: number;
  active_slot?: string;
  state: string;
  slots: Record<string, Release>;
  releases: Release[];
};
export type Service = {
  name: string;
  slot: string;
  image: string;
  template_id: string;
  compose_revision?: string;
  environment_revision: string;
};
export type Job = {
  job_id: string;
  project_id: string;
  action: string;
  status: string;
  phase: string;
  error_code?: string;
  warning_code?: string;
  actor_id: string;
  request_id: string;
  created_at: string;
  finished_at?: string;
};
export type Inventory = {
  last_sync_at?: string;
  projects_observed_at?: string;
  caddy_observed_at?: string;
  docker_observed_at?: string;
  projects: {
    id: string;
    name: string;
    environment: string;
    managed: boolean;
    present: boolean;
    compose_files: string[];
    services: { name: string; image?: string; published_ports: number[] }[];
    warnings: string[];
  }[];
  sites: { host_matcher: string; upstreams: string[]; project_ids: string[] }[];
  reserved_ports: number[];
  warnings: string[];
};
export type MigrationAssessment = {
  project_id: string;
  status: "blocked" | "candidate";
  source_sha256?: string;
  checks: { code: string; status: "passed" | "blocked" }[];
  execution_available: boolean;
};
export type Profile = {
  name: string;
  origin: string;
  server_id: string;
  key_id: string;
  actor_id: string;
  server_certificate_sha256: string;
  server_ip?: string | null;
  ssh_port?: number | null;
  ssh_fingerprint?: string | null;
};
export type SetupPreview = {
  id: string;
  server_ip: string;
  ssh_port: number;
  host_sha256: string;
  ubuntu: string;
  docker_installed: boolean;
  caddy_installed: boolean;
  inventory_only: boolean;
  binary_sha256: string;
};
export type Session = {
  unlocked: boolean;
  profile: Profile | null;
  jobs: string[];
  enrolled?: boolean;
};
export type Pending = {
  project: string;
  action: string;
  request_id: string;
  idempotency: string;
  job_id?: string;
} | null;
export type Read =
  | { kind: "projects" | "inventory" | "port" }
  | { kind: "migration"; project: string }
  | { kind: "audit"; after: number }
  | {
      kind: "project";
      project: string;
      view: "status" | "releases" | "services" | "domains";
    }
  | {
      kind: "logs";
      project: string;
      service: string;
      slot: string;
      tail: number;
      since: string;
    }
  | { kind: "job"; job: string };
