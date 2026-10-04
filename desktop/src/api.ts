import { invoke, isTauri } from "@tauri-apps/api/core";
import type { Read, Session, Pending, SetupPreview } from "./types";
export const native = isTauri();
export const sessionInfo = () => invoke<Session>("session_info");
export const unlock = () => invoke<Session>("unlock");
export const enroll = () => invoke<Session>("enroll");
export const lock = () => invoke<void>("lock_session");
export const forget = () => invoke<void>("forget_device");
export const read = <T>(read: Read) => invoke<T>("read_api", { read });
export const mutate = (mutation: unknown) =>
  invoke<{ job_id: string }>("mutate", { mutation });
export const reconcileJob = (job: string) =>
  invoke<{ status: string; error: string; project_state: string }>(
    "reconcile_job",
    { job },
  );
export const resolvePending = () =>
  invoke<{
    outcome: "none" | "not_sent" | "not_applied" | "applied";
    job_id?: string;
    status?: string;
  }>("resolve_pending");
export const readEnv = (project: string) =>
  invoke<{ release_id: string; env_file: string }>("read_env", { project });
export const retry = () => invoke<{ job_id: string }>("retry_pending");
export const pendingInfo = () => invoke<Pending>("pending_info");
export const importCompose = () => invoke<string | null>("import_compose");
export const importEnv = () => invoke<string | null>("import_env");
export const inspectServer = (request: {
  os: "ubuntu";
  server_ip: string;
  ssh_port: number;
  name: string;
  import_policy: boolean;
}) => invoke<SetupPreview>("setup_inspect", { request });
export const installServer = (id: string) =>
  invoke<Session>("setup_install", { id });
export const resumeSetup = () => invoke<Session>("setup_resume");
export const cancelSetup = () => invoke<void>("setup_cancel");
export type ServerUpdatePreview = {
  source_commit: string;
  candidate_version: string;
  installed_version: string | null;
  installed_commit: string | null;
  version_status:
    | "current"
    | "update_available"
    | "legacy"
    | "legacy_bundle"
    | "server_newer"
    | "same_version_different_build"
    | "bundle_unversioned";
  candidate_dockyard: string;
  candidate_dockyardctl: string;
  installed_dockyard: string;
  installed_dockyardctl: string;
  update_available: boolean;
  jobs: {
    sha256: string;
    active_count: number;
    recovery_count: number;
    active_jobs: UpdateJob[];
    recovery_jobs: UpdateJob[];
  };
};
export type UpdateJob = {
  job_id: string;
  project_id: string;
  status: "queued" | "running" | "recovery_required";
  action: string;
  phase: string;
  error_code: string;
};
export const checkServerUpdate = () =>
  invoke<ServerUpdatePreview>("server_update_check");
export const applyServerUpdate = (expected: ServerUpdatePreview) =>
  invoke<string>("server_update_apply", { expected });
export const forgetRootPassword = () =>
  invoke<void>("terminal_forget_password");
export type ServerAccessReport = {
  root_ssh: boolean;
  update_directory: "ready" | "can_prepare" | "manual_review";
  installed_binaries: boolean;
  database: boolean;
  service_active: boolean;
  checks: ServerAccessCheck[];
  recent_jobs: ServerAccessJob[];
};
export type ServerAccessCheck = {
  id: string;
  label: string;
  status: "ready" | "warning" | "failed";
  detail: string;
  repair?: string | null;
};
export type ServerAccessJob = {
  job_id: string;
  project_id: string;
  action: string;
  status: string;
  phase: string;
  error_code: string;
  finished_at: string;
};
export const checkServerAccess = () =>
  invoke<ServerAccessReport>("server_access_check");
export const prepareServerAccess = () =>
  invoke<ServerAccessReport>("server_access_prepare");

export const enableComposeManagement = () =>
  invoke<void>("server_compose_enable");

export type BlueGreenInput = {
  expected_release_id: string;
  compose_yaml: string;
  env_file: string;
  route_service: string;
  route_port: number;
  readiness_path: string;
  review_sha256?: string;
};
export type BlueGreenReview = {
  review_sha256: string;
  blue_port: number;
  green_port: number;
  domains: string[];
  route_service: string;
  route_port: number;
  imports_routes: boolean;
};
export const previewBlueGreen = (project: string, data: BlueGreenInput) =>
  invoke<BlueGreenReview>("preview_blue_green", { project, data });

export type ServiceUpdateInput = {
  expected_release_id: string;
  service: string;
  mode: "seamless" | "restart";
  container_port?: number;
  readiness_path?: string;
  review_sha256?: string;
};
export type ServiceUpdateReview = {
  review_sha256: string;
  service: string;
  mode: "seamless" | "restart";
  domains: string[];
  container_port: number;
  host_port: number;
  imports_routes: boolean;
  dependencies_unchanged: boolean;
};
export const previewServiceUpdate = (
  project: string,
  data: ServiceUpdateInput,
) => invoke<ServiceUpdateReview>("preview_service_update", { project, data });

export type RouteSetupInput = {
  expected_release_id: string;
  domains: string[];
  service: string;
  container_port: number;
  review_sha256?: string;
};
export type RouteSetupReview = {
  review_sha256: string;
  domains: string[];
  service: string;
  container_port: number;
  upstream: string;
  imports_routes: boolean;
  containers_unchanged: boolean;
};
export const previewRouteSetup = (project: string, data: RouteSetupInput) =>
  invoke<RouteSetupReview>("preview_route_setup", { project, data });
