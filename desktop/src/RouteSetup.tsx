import { useEffect, useRef, useState } from "react";
import { Fingerprint, ShieldCheck } from "lucide-react";
import Button from "./Button";
import Select from "./Select";
import * as api from "./api";
import { invoke } from "@tauri-apps/api/core";
import ServiceDomainPicker, {
  type ServiceDNSInput,
  type ServiceDNSReview,
} from "./ServiceDomainPicker";
import { ConnectionHealth } from "./ServerRepair";
import ServerUpdater from "./ServerUpdater";
import type { Project, Service } from "./types";

function explain(error: unknown) {
  const message = String(error);
  const explanations: Record<string, string> = {
    HTTP_404: "Update the Dockyard service on the VPS to enable route setup.",
    ROUTE_SETUP_UNAVAILABLE:
      "Update the Dockyard service to connect domains to individual services.",
    ROUTE_REVIEW_CHANGED:
      "The project, Caddy route or upstream changed. Review again.",
    CONFIGURATION_CHANGED:
      "The saved release changed. Refresh the project and review again.",
    ROUTE_DOMAINS_CHANGED:
      "Keep every detected domain when importing the existing Caddy site.",
    RECOVERY_REQUIRED:
      "Resolve the interrupted operation in Deployments before configuring this route.",
    PROJECT_NOT_RUNNING:
      "Dockyard must verify a running release before configuring its route.",
    PROJECT_BUSY:
      "Wait for the current operation to finish, then review again.",
    CONTAINER_OWNERSHIP_UNKNOWN:
      "The running containers differ from Dockyard’s recorded identities. Inspect the manual VPS changes before proceeding.",
    BLUE_GREEN_ROUTE_REVIEW_REQUIRED:
      "The existing Caddy site uses rules that require manual inspection. Dockyard can import only a plain local reverse-proxy site.",
    BLUE_GREEN_ROUTE_TARGET_MISMATCH:
      "The selected service and container port do not match the existing Caddy upstream.",
    CADDY_CONFIG_WRITE_REQUIRED:
      "Dockyard needs access to its Caddy folders. Use the repair below, then review again.",
    CADDY_UNAVAILABLE:
      "The private Caddy connection is unavailable. Check and repair it below.",
    COMPOSE_ACCESS_REQUIRED:
      "Enable Compose management using the connection check below.",
    PUBLISHED_ROUTE_FIXED:
      "This route uses the existing published port. Change the Compose configuration through a reviewed deployment to move it.",
    STATE_DIVERGED:
      "Saved and live Caddy routes differ. Review the repair below, then retry the connection.",
    DOMAIN_IN_USE:
      "This domain is already assigned to another service. Choose another hostname.",
  };
  return (
    Object.entries(explanations).find(([code]) =>
      message.includes(code),
    )?.[1] ?? message.replace(/^Error: /, "")
  );
}

export default function RouteSetup({
  project,
  services,
  loading,
  unavailable,
  blocked,
  preview,
  execute,
  onClose,
  openDeployments,
  refresh,
  initialService,
  initialDomains,
  onBusyChange,
}: {
  project: Project;
  services: Service[];
  loading: boolean;
  unavailable: boolean;
  blocked: boolean;
  preview: boolean;
  execute: (mutation: unknown) => Promise<void>;
  onClose: () => void;
  openDeployments?: () => void;
  refresh: () => void;
  initialService?: string;
  initialDomains?: string[];
  onBusyChange?: (busy: boolean) => void;
}) {
  const candidates = services.filter(
    (service) => service.slot === project.active_slot,
  );
  const savedRoute = project.service_routes?.find(
    (route) => route.service === initialService,
  );
  const primaryService =
    project.route_service || project.published_route?.service;
  const siblingDomains = new Set(
    project.service_routes?.flatMap((route) => route.domains) ?? [],
  );
  const [service, setService] = useState(initialService ?? "");
  const [portEdited, setPortEdited] = useState(false);
  const preferredPort = (name: string) =>
    project.service_routes?.find((route) => route.service === name)
      ?.container_port ??
    (name === primaryService
      ? project.route_port || project.published_route?.container_port
      : undefined) ??
    candidates.find((item) => item.name === name)?.container_ports?.[0];
  const [port, setPort] = useState(
    String(
      savedRoute?.container_port ??
        (initialService === primaryService
          ? project.route_port || project.published_route?.container_port
          : undefined) ??
        candidates.find((s) => s.name === initialService)
          ?.container_ports?.[0] ??
        "",
    ),
  );
  const [domains, setDomains] = useState(
    (
      initialDomains ??
      savedRoute?.domains ??
      (initialService && initialService === primaryService
        ? project.domains.filter((name) => !siblingDomains.has(name))
        : project.domains.length
          ? []
          : (project.external_domains ?? []))
    ).join(", "),
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [dns, setDNS] = useState<ServiceDNSInput>();
  const [dnsReview, setDNSReview] = useState<ServiceDNSReview>();
  const [dnsDone, setDNSDone] = useState("");
  useEffect(() => {
    onBusyChange?.(busy);
  }, [busy, onBusyChange]);
  const [checked, setChecked] = useState<{
    key: string;
    review: api.RouteSetupReview;
  }>();
  const sequence = useRef(0);
  const selected = candidates.find((item) => item.name === service);
  useEffect(() => {
    if (!selected && candidates.length) {
      const first =
        candidates.find((item) => item.name === primaryService) ??
        candidates.find((item) => item.container_ports?.length) ??
        candidates[0];
      setService(first.name);
      setPort(String(preferredPort(first.name) ?? ""));
    } else if (selected && !port && !portEdited) {
      setPort(String(preferredPort(selected.name) ?? ""));
    }
  }, [JSON.stringify(candidates), selected, port, portEdited]);
  const data: api.RouteSetupInput = {
    expected_release_id: project.slots[project.active_slot ?? "blue"]?.id ?? "",
    domains: domains.split(/[\s,]+/).filter(Boolean),
    service,
    container_port: Number(port),
  };
  const key = JSON.stringify([project, data, dns]);
  const review = checked?.key === key ? checked.review : undefined;
  const valid =
    !!selected &&
    !!data.expected_release_id &&
    data.domains.length > 0 &&
    data.domains.length <= 10 &&
    (!dns || (!!dns.name && !!dns.provider && !!dns.zone)) &&
    Number.isInteger(data.container_port) &&
    data.container_port > 0 &&
    data.container_port <= 65535;
  useEffect(() => {
    sequence.current++;
    setChecked(undefined);
    setDNSReview(undefined);
    setError("");
    setBusy(false);
  }, [key, blocked]);
  useEffect(
    () => () => {
      sequence.current++;
    },
    [],
  );
  const disabled =
    blocked || project.state !== "running" || loading || unavailable;

  async function check() {
    if (!valid || disabled) return;
    const current = ++sequence.current;
    setBusy(true);
    setError("");
    setChecked(undefined);
    try {
      const result = preview
        ? {
            review_sha256: "sample",
            domains: data.domains,
            service,
            container_port: data.container_port,
            upstream: "127.0.0.1:3031",
            imports_routes: !!project.external_domains?.length,
            containers_unchanged: true,
          }
        : await api.previewRouteSetup(project.id, data);
      const dnsResult = dns
        ? await invoke<ServiceDNSReview>("provider_service_dns_preview", {
            input: dns,
          })
        : undefined;
      if (sequence.current === current) {
        setChecked({ key, review: result });
        setDNSReview(dnsResult);
      }
    } catch (error) {
      if (sequence.current === current) setError(String(error));
    } finally {
      if (sequence.current === current) setBusy(false);
    }
  }
  async function apply() {
    if (!review || preview || disabled) return;
    const current = ++sequence.current;
    setBusy(true);
    setError("");
    try {
      if (dns && dnsReview) {
        await invoke("provider_service_dns_apply", {
          input: dns,
          expected: dnsReview.review_sha256,
        });
        if (sequence.current !== current) return;
        setDNSDone(`${dnsReview.name} → ${dnsReview.server_ip}`);
      }
      await execute({
        action: "route_setup",
        project: project.id,
        data: { ...data, review_sha256: review.review_sha256 },
      });
      if (sequence.current === current) onClose();
    } catch (error) {
      if (sequence.current === current) {
        setChecked(undefined);
        setError(String(error));
      }
    } finally {
      if (sequence.current === current) setBusy(false);
    }
  }
  const notRunning = project.state !== "running";
  const [starting, setStarting] = useState(false);
  // One clear blocker at a time, each with the action that resolves it.
  if (blocked)
    return (
      <div className="route-setup route-blocked">
        <span>Another operation is in progress for this project.</span>
        {openDeployments && (
          <Button
            type="button"
            className="button small"
            onClick={openDeployments}
          >
            View deployments
          </Button>
        )}
      </div>
    );
  if (notRunning)
    return (
      <div className="route-setup route-blocked">
        <span>Start {project.app_id} to connect a domain.</span>
        <Button
          type="button"
          className="button small primary"
          disabled={starting || preview}
          onClick={async () => {
            setStarting(true);
            try {
              await execute({ action: "start", project: project.id });
            } catch (e) {
              setError(explain(e));
            } finally {
              setStarting(false);
            }
          }}
        >
          {starting ? "Starting…" : "Start"}
        </Button>
        {error && <small className="route-error">{error}</small>}
      </div>
    );
  return (
    <div className="route-setup">
      <ServiceDomainPicker
        service={service}
        domains={domains}
        setDomains={setDomains}
        onDNS={setDNS}
        disabled={busy || disabled}
        preview={preview}
      />
      {unavailable && (
        <p className="alert error" role="alert">
          Couldn't read services.{" "}
          <Button type="button" className="text-button" onClick={refresh}>
            Retry
          </Button>
        </p>
      )}
      <div className="route-fields">
        <div className="route-service">
          <Select
            label="Service"
            value={service}
            disabled={busy || loading || !candidates.length}
            options={candidates.map((item) => ({
              value: item.name,
              label: item.name,
            }))}
            onValueChange={(value) => {
              setService(value);
              setPortEdited(false);
              setPort(String(preferredPort(value) ?? ""));
            }}
          />
        </div>
        <label className="route-port">
          <span>Port</span>
          <input
            type="number"
            min={1}
            max={65535}
            value={port}
            disabled={busy}
            title="The port the app listens on inside its container"
            onChange={(event) => {
              setPortEdited(true);
              setPort(event.target.value);
            }}
          />
        </label>
        {review ? (
          <Button
            type="button"
            className="button primary"
            disabled={busy || preview || disabled}
            onClick={() => void apply()}
          >
            <Fingerprint size={15} />
            {busy ? "Connecting…" : "Connect"}
          </Button>
        ) : (
          <Button
            type="button"
            className="button primary"
            disabled={busy || !valid || disabled}
            onClick={() => void check()}
          >
            {busy ? "Checking…" : "Review"}
          </Button>
        )}
      </div>
      {selected?.container_ports?.length ? (
        <p className="muted">
          Detected container ports: {selected.container_ports.join(", ")}.
          Dockyard chooses the host port.
        </p>
      ) : (
        <p className="muted">
          Enter the port this service listens on inside its container.
        </p>
      )}
      {error && (
        <>
          <p className="route-error" role="alert">
            {explain(error)}
          </p>
          {/STATE_DIVERGED|CADDY_|COMPOSE_ACCESS_REQUIRED|SCOPE_REQUIRED/.test(
            error,
          ) &&
            !preview && (
              <ConnectionHealth
                onReady={() => {
                  setChecked(undefined);
                  setError("");
                }}
                disabled={busy}
              />
            )}
          {/HTTP_404|ROUTE_SETUP_UNAVAILABLE/.test(error) && !preview && (
            <ServerUpdater openOperations={openDeployments} />
          )}
        </>
      )}
      {dnsDone && (
        <p className="muted" role="status">
          DNS verified: {dnsDone}.{" "}
          {error ? "Review and retry the Caddy connection." : ""}
        </p>
      )}
      {review && (
        <p className="route-review">
          <ShieldCheck size={14} />
          {review.domains.join(", ")} → {review.service}:{review.container_port}
          <span>
            {review.imports_routes
              ? "Moves the existing Caddy site"
              : "New Caddy route"}{" "}
            ·
            {review.containers_unchanged
              ? "containers unchanged"
              : "controlled deployment to publish the service port"}
          </span>
        </p>
      )}
      {review && dnsReview && (
        <p className="route-review">
          <ShieldCheck size={14} />
          {dnsReview.action === "reuse"
            ? "DNS already points to this VPS"
            : dnsReview.action === "update"
              ? `Update DNS from ${dnsReview.previous}`
              : "Create Cloudflare DNS"}
          : {dnsReview.record_type} {dnsReview.name} → {dnsReview.server_ip} ·{" "}
          {dnsReview.proxied ? "Proxied" : "DNS only"}
        </p>
      )}
    </div>
  );
}
