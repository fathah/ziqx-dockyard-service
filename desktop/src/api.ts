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
export const retry = () => invoke<{ job_id: string }>("retry_pending");
export const pendingInfo = () => invoke<Pending>("pending_info");
export const importCompose = () => invoke<string | null>("import_compose");
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
