import type { Service } from "./types";

const legacyReplicaRestriction =
  "Individual updates currently support one container per service.";

export function serviceUpdatesNeedServerUpgrade(service: Service): boolean {
  return (
    service.update_reason === legacyReplicaRestriction ||
    (service.updatable === undefined &&
      service.seamless === undefined &&
      !service.update_reason &&
      !service.container_ports)
  );
}

export function serviceUpdateUnavailableReason(service: Service): string {
  if (serviceUpdatesNeedServerUpgrade(service))
    return "Update Dockyard in Server details → Updates, then refresh Services.";
  return (
    service.update_reason ||
    "Individual updates are unavailable for this service."
  );
}
