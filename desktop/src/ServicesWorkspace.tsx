import { useEffect, useState } from "react";
import {
  ChevronRight,
  Globe2,
  RefreshCw,
  Search,
  Terminal,
  X,
} from "lucide-react";
import Button from "./Button";
import CopyButton from "./CopyButton";
import { serviceDomains } from "./domainRoutes";
import type { ImageTask } from "./api";
import type { Project, Service } from "./types";
import "./ServicesWorkspace.css";

export const serviceKey = (service: Service) =>
  `${service.slot}/${service.name}`;

export function serviceDisplayState(project: Project, service: Service) {
  if (service.slot !== project.active_slot) return "Recorded";
  if (project.state !== "running") return project.state.replaceAll("_", " ");
  if (service.containers == null) return "Not checked";
  return service.containers.length ? "Running" : "Not running";
}

export default function ServicesWorkspace({
  project,
  services,
  loading,
  error,
  images,
  selectedKey,
  onSelect,
  blockedReason,
  controlsBlocked,
  onRefresh,
  onUpdate,
  onConnect,
  onLogs,
  onShell,
}: {
  project: Project;
  services: Service[];
  loading: boolean;
  error: boolean;
  images?: ImageTask;
  selectedKey: string;
  onSelect: (key: string) => void;
  blockedReason: (service: Service) => string;
  controlsBlocked: boolean;
  onRefresh: () => void;
  onUpdate: (service: Service) => void;
  onConnect: (service: Service) => void;
  onLogs: (service: Service) => void;
  onShell?: (service: Service) => void;
}) {
  const [query, setQuery] = useState("");
  useEffect(() => {
    const target = services.find(
      (service) => serviceKey(service) === selectedKey,
    );
    // A dependency or update notice can select a service outside the filter.
    // Clear that filter only when the selection changes, while typing stays local.
    if (target)
      setQuery((current) =>
        `${target.name} ${target.image}`
          .toLowerCase()
          .includes(current.trim().toLowerCase())
          ? current
          : "",
      );
  }, [selectedKey, services]);
  const sorted = [...services].sort(
    (a, b) =>
      Number(b.slot === project.active_slot) -
        Number(a.slot === project.active_slot) ||
      a.name.localeCompare(b.name) ||
      a.slot.localeCompare(b.slot),
  );
  const selected =
    sorted.find((service) => serviceKey(service) === selectedKey) ?? sorted[0];
  const search = query.trim().toLowerCase();
  const visible = sorted.filter((service) =>
    `${service.name} ${service.image}`.toLowerCase().includes(search),
  );
  const active = selected?.slot === project.active_slot;
  const blocked = selected && active ? blockedReason(selected) : "";
  const domains =
    selected && active ? serviceDomains(project, selected.name) : [];
  const usedBy = selected
    ? sorted.filter(
        (service) =>
          service.slot === selected.slot &&
          service.depends_on?.includes(selected.name),
      )
    : [];
  const imageState = (service: Service) =>
    service.slot === project.active_slot
      ? images?.services?.find((item) => item.service === service.name)?.status
      : undefined;
  const imageNote =
    selected && imageState(selected) === "available"
      ? "A new image is available."
      : selected && imageState(selected) === "current"
        ? "Image is up to date. Saved environment changes can still be applied."
        : selected && imageState(selected) === "pinned"
          ? "Image pinned to a digest."
          : images?.status === "checking"
            ? "Checking for image updates…"
            : images?.status === "failed"
              ? "Image update check unavailable."
              : "Image recorded at deployment.";

  function dependency(name: string) {
    const target = sorted.find(
      (service) => service.name === name && service.slot === selected?.slot,
    );
    return (
      <Button
        key={name}
        type="button"
        className="service-dependency-chip"
        disabled={!target}
        onClick={() => target && onSelect(serviceKey(target))}
        aria-label={`Select ${name} service`}
      >
        {name}
      </Button>
    );
  }

  return (
    <div className="services-workspace">
      <section className="panel service-browser" aria-label="Services">
        <div className="service-browser-toolbar">
          <h2>
            Services <span>{loading ? "" : services.length}</span>
          </h2>
          <div className="service-browser-tools">
            <label className="service-search">
              <Search size={15} aria-hidden="true" />
              <input
                aria-label="Search services"
                placeholder="Search services"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
              {query && (
                <Button
                  type="button"
                  className="service-search-clear"
                  aria-label="Clear service search"
                  onClick={() => setQuery("")}
                >
                  <X size={14} />
                </Button>
              )}
            </label>
            <Button
              type="button"
              className="button service-refresh"
              variant="ghost"
              disabled={loading}
              onClick={onRefresh}
              aria-label="Refresh services"
              title="Refresh services"
            >
              <RefreshCw
                size={17}
                className={loading ? "service-refreshing" : ""}
              />
            </Button>
          </div>
        </div>
        <div className="service-list-heading" aria-hidden="true">
          <span>Name</span>
          <span>Ports</span>
          <span>Status</span>
          <span />
        </div>
        {loading ? (
          <div className="service-list-message" role="status">
            Loading services…
          </div>
        ) : error ? (
          <div className="service-list-message" role="status">
            Could not load services. Refresh to try again.
          </div>
        ) : visible.length ? (
          <ul className="service-list" aria-label="Project services">
            {visible.map((service) => {
              const chosen = serviceKey(service) === serviceKey(selected);
              const state = serviceDisplayState(project, service);
              return (
                <li key={serviceKey(service)}>
                  <Button
                    type="button"
                    className={`service-list-row ${chosen ? "is-selected" : ""}`}
                    aria-pressed={chosen}
                    aria-label={`Select ${service.name} service`}
                    onClick={() => onSelect(serviceKey(service))}
                  >
                    <span className="service-list-identity">
                      <span className="service-avatar" aria-hidden="true">
                        {service.name[0]?.toUpperCase()}
                      </span>
                      <span className="service-list-label">
                        <span className="service-list-name">
                          <strong>{service.name}</strong>
                          {(service.containers?.length ?? 0) > 1 && (
                            <span className="service-replica-count">
                              ×{service.containers!.length}
                            </span>
                          )}
                          {service.slot !== project.active_slot && (
                            <span className="service-replica-count">
                              Standby
                            </span>
                          )}
                          {imageState(service) === "available" && (
                            <span className="service-update-badge">Update</span>
                          )}
                        </span>
                        <code title={service.image}>
                          {service.image || "Built from Compose"}
                        </code>
                      </span>
                    </span>
                    <span className="service-list-ports">
                      {service.host_ports?.length
                        ? service.host_ports.map((port) => `:${port}`).join(" ")
                        : "—"}
                    </span>
                    <span
                      className="service-list-status"
                      title="Container presence and recorded project state. Verify live health in Overview."
                    >
                      <i
                        data-running={state === "Running"}
                        aria-hidden="true"
                      />
                      {state}
                    </span>
                    <ChevronRight
                      size={15}
                      className="service-row-chevron"
                      aria-hidden="true"
                    />
                  </Button>
                </li>
              );
            })}
          </ul>
        ) : (
          <div className="service-list-message" role="status">
            <strong>
              {query ? "No matching services" : "No services recorded"}
            </strong>
            <p>
              {query
                ? "Try a different name or image."
                : "Deploy your first Compose stack to see its services."}
            </p>
            {query && (
              <Button
                type="button"
                className="text-button"
                onClick={() => setQuery("")}
              >
                Clear search
              </Button>
            )}
          </div>
        )}
        <p className="service-browser-footer">
          Update one service at a time. Its dependencies keep running.
        </p>
      </section>
      {selected && !loading && !error && (
        <aside
          className="panel service-inspector"
          aria-label={`${selected.name} service details`}
        >
          <div className="service-inspector-top">
            <header className="service-inspector-heading">
              <span className="service-avatar" aria-hidden="true">
                {selected.name[0]?.toUpperCase()}
              </span>
              <div>
                <h2>{selected.name}</h2>
                <p className="service-list-status">
                  <i
                    data-running={
                      serviceDisplayState(project, selected) === "Running"
                    }
                    aria-hidden="true"
                  />
                  {serviceDisplayState(project, selected)}
                  {selected.containers != null &&
                    selected.containers.length > 0 &&
                    ` · ${selected.containers.length} ${selected.containers.length === 1 ? "container" : "instances"}`}
                </p>
              </div>
            </header>
            <div className="service-inspector-actions">
              <Button
                type="button"
                className="button"
                onClick={() => onLogs(selected)}
              >
                Logs
              </Button>
              <Button
                type="button"
                className="button"
                disabled={
                  !onShell ||
                  !active ||
                  project.state !== "running" ||
                  selected.containers?.length === 0
                }
                onClick={() => onShell?.(selected)}
              >
                <Terminal size={14} /> Terminal
              </Button>
              {project.mode === "compose" && active && (
                <Button
                  type="button"
                  className="button primary"
                  disabled={Boolean(blocked)}
                  title={blocked || `Pull & update ${selected.name}`}
                  onClick={() => onUpdate(selected)}
                >
                  Update
                </Button>
              )}
            </div>
            <div
              className={`service-update-guidance ${blocked ? "is-blocked" : ""}`}
              role="status"
            >
              {blocked ||
                (active && selected.seamless
                  ? "Seamless updates available. New replicas are checked before traffic switches."
                  : active && selected.update_reason
                    ? selected.update_reason
                    : imageNote)}
            </div>
          </div>
          <dl className="service-inspector-facts">
            <div>
              <dt>Image</dt>
              <dd>
                <code>{selected.image || "Built from Compose"}</code>
              </dd>
            </div>
            <div>
              <dt>Host ports</dt>
              <dd>
                {selected.host_ports == null
                  ? "Not checked"
                  : selected.host_ports.length
                    ? selected.host_ports.map((port) => `:${port}`).join(" · ")
                    : "Internal only"}
              </dd>
            </div>
            <div>
              <dt>Depends on</dt>
              <dd className="service-dependency-list">
                {selected.depends_on == null
                  ? "Not recorded"
                  : selected.depends_on.length
                    ? selected.depends_on.map(dependency)
                    : "None"}
              </dd>
            </div>
            <div>
              <dt>Used by</dt>
              <dd className="service-dependency-list">
                {usedBy.length
                  ? usedBy.map((service) => dependency(service.name))
                  : "None"}
              </dd>
            </div>
            {selected.load_balancer && (
              <div>
                <dt>Load balancer</dt>
                <dd>{dependency(selected.load_balancer)}</dd>
              </div>
            )}
            {domains.length > 0 && (
              <div>
                <dt>Domains</dt>
                <dd className="service-domain-values">
                  {domains.map((domain) => (
                    <span key={domain}>{domain}</span>
                  ))}
                </dd>
              </div>
            )}
          </dl>
          <section
            className="service-inspector-containers"
            aria-label="Containers"
          >
            <h3>
              Containers <span>{selected.containers?.length ?? ""}</span>
            </h3>
            {selected.containers?.length ? (
              <ul>
                {selected.containers.map((container) => (
                  <li key={container.id}>
                    <div className="service-container-identity">
                      <code title={container.name || container.id}>
                        {container.name || container.id}
                      </code>
                      <span title={container.id}>
                        {container.id.slice(0, 12)}
                      </span>
                    </div>
                    <div className="service-container-copy">
                      <CopyButton
                        text={container.name || container.id}
                        label="Name"
                      />
                      <CopyButton text={container.id} label="ID" />
                    </div>
                  </li>
                ))}
              </ul>
            ) : (
              <p>
                {selected.containers == null
                  ? "Container identity unavailable."
                  : "No running containers."}
              </p>
            )}
          </section>
          <div className="service-inspector-footer">
            <span>{imageNote}</span>
            {project.mode === "compose" && active && (
              <Button
                type="button"
                className="text-button"
                disabled={controlsBlocked}
                onClick={() => onConnect(selected)}
              >
                <Globe2 size={14} />
                {domains.length ? "Manage domains" : "Connect domain"}
              </Button>
            )}
          </div>
        </aside>
      )}
    </div>
  );
}
