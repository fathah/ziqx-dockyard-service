import type { Project } from "./types";

export function domainTarget(project: Project, hostname: string) {
  const route = project.service_routes?.find((item) =>
    item.domains.includes(hostname),
  );
  const service =
    route?.service || project.route_service || project.published_route?.service;
  if (!service) return undefined;
  const container_port =
    route?.container_port ||
    project.route_port ||
    project.published_route?.container_port;
  const host_port =
    project.service_instances?.[service]?.port ||
    route?.host_port ||
    (project.active_slot === "green" ? project.green_port : project.blue_port);
  return { service, container_port, host_port };
}

export function serviceDomains(project: Project, service: string) {
  return project.domains.filter(
    (hostname) => domainTarget(project, hostname)?.service === service,
  );
}

export function routeReleaseId(project: Project) {
  return (
    (
      (project.state !== "running" && project.releases.at(-1)) ||
      project.slots[project.active_slot ?? "blue"]
    )?.id ?? ""
  );
}
