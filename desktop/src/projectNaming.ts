import type { Environment, Project } from "./types";

type ExistingProject = Pick<Project, "id" | "app_id" | "environment">;

// An explicit flavor keeps the application's exact identity, including legacy
// IDs that are longer than the name generator's 36-character base.
export function flavorIdentity(
  appID: string,
  environment: Environment | "",
  projects: ExistingProject[],
  observedIDs: string[] = [],
) {
  if (
    !appID ||
    !environment ||
    projects.some((p) => p.app_id === appID && p.environment === environment)
  )
    return null;
  const suffix = `-${environment}`;
  const prefix = `${appID.slice(0, 48 - suffix.length).replace(/-+$/, "")}${suffix}`;
  const used = new Set([...projects.map((p) => p.id), ...observedIDs]);
  let id = prefix;
  for (let n = 2; used.has(id); n++) {
    const collision = `-${n}`;
    id = `${prefix.slice(0, 48 - collision.length).replace(/-+$/, "")}${collision}`;
  }
  return { id, app_id: appID, adjusted: id !== prefix };
}

export function projectIdentity(
  name: string,
  environment: Environment | "",
  projects: ExistingProject[],
  observedIDs: string[] = [],
) {
  if (!name.trim() || !environment) return null;
  let base = name
    .normalize("NFKD")
    .replace(/\p{M}/gu, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "");
  if (!base) base = "project";
  if (!/^[a-z]/.test(base)) base = `app-${base}`;
  // Leave room for every environment, including "-development".
  base = base.slice(0, 36).replace(/-+$/, "");
  const usedApps = new Set(projects.map((p) => p.app_id));
  const usedIDs = new Set([...projects.map((p) => p.id), ...observedIDs]);
  const occupied = (app: string) =>
    projects.some((p) => p.app_id === app && p.environment === environment) ||
    observedIDs.includes(app);
  let appID = base;
  if (occupied(appID)) {
    for (let n = 2; ; n++) {
      const suffix = `-${n}`;
      appID = `${base.slice(0, 36 - suffix.length).replace(/-+$/, "")}${suffix}`;
      if (!usedApps.has(appID) && !observedIDs.includes(appID)) break;
    }
  }
  const prefix = `${appID}-${environment}`;
  let id = prefix;
  for (let n = 2; usedIDs.has(id); n++) {
    const suffix = `-${n}`;
    id = `${prefix.slice(0, 48 - suffix.length).replace(/-+$/, "")}${suffix}`;
  }
  return { id, app_id: appID, adjusted: appID !== base || id !== prefix };
}
