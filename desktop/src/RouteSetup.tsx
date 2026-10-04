import { useEffect, useRef, useState } from "react";
import { Fingerprint, ShieldCheck } from "lucide-react";
import Button from "./Button";
import Select from "./Select";
import * as api from "./api";
import type { Project, Service } from "./types";

function explain(error: unknown) {
  const message = String(error);
  const explanations: Record<string, string> = {
    HTTP_404: "Update the Dockyard service on the VPS to enable route setup.",
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
      "Check Caddy folder write access in Server details → Access & health.",
    COMPOSE_ACCESS_REQUIRED:
      "Enable Compose management in Server details → Access & health first.",
    PUBLISHED_ROUTE_FIXED:
      "This route uses the existing published port. Change the Compose configuration through a reviewed deployment to move it.",
    STATE_DIVERGED:
      "The saved and live Caddy configurations differ. Check them in Server details → Access & health.",
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
}) {
  const candidates = services.filter(
    (service) => service.slot === project.active_slot,
  );
  const [service, setService] = useState("");
  const [port, setPort] = useState("");
  const [domains, setDomains] = useState(
    (project.external_domains ?? []).join(", "),
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [checked, setChecked] = useState<{
    key: string;
    review: api.RouteSetupReview;
  }>();
  const sequence = useRef(0);
  const selected = candidates.find((item) => item.name === service);
  useEffect(() => {
    if (!selected && candidates.length) {
      const first =
        candidates.find((item) => item.container_ports?.length) ??
        candidates[0];
      setService(first.name);
      setPort(String(first.container_ports?.[0] ?? ""));
    }
  }, [JSON.stringify(candidates), selected]);
  const data: api.RouteSetupInput = {
    expected_release_id: project.slots[project.active_slot ?? "blue"]?.id ?? "",
    domains: domains.split(/[\s,]+/).filter(Boolean),
    service,
    container_port: Number(port),
  };
  const key = JSON.stringify([project, data]);
  const review = checked?.key === key ? checked.review : undefined;
  const valid =
    !!selected &&
    !!data.expected_release_id &&
    data.domains.length > 0 &&
    data.domains.length <= 10 &&
    Number.isInteger(data.container_port) &&
    data.container_port > 0 &&
    data.container_port <= 65535;
  useEffect(() => {
    sequence.current++;
    setChecked(undefined);
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
      if (sequence.current === current) setChecked({ key, review: result });
    } catch (error) {
      if (sequence.current === current) setError(explain(error));
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
      await execute({
        action: "route_setup",
        project: project.id,
        data: { ...data, review_sha256: review.review_sha256 },
      });
      if (sequence.current === current) onClose();
    } catch (error) {
      if (sequence.current === current) {
        setChecked(undefined);
        setError(explain(error));
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
          <Button type="button" className="button small" onClick={openDeployments}>
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
      {unavailable && (
        <p className="alert error" role="alert">
          Couldn't read services.{" "}
          <Button type="button" className="text-button" onClick={refresh}>
            Retry
          </Button>
        </p>
      )}
      <div className="route-fields">
        <label className="route-domain">
          <span>Domain</span>
          <input
            value={domains}
            placeholder="app.example.com"
            readOnly={!!project.external_domains?.length}
            disabled={busy}
            onChange={(event) => setDomains(event.target.value)}
          />
        </label>
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
              setPort(
                String(
                  candidates.find((item) => item.name === value)
                    ?.container_ports?.[0] ?? "",
                ),
              );
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
            onChange={(event) => setPort(event.target.value)}
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
      {error && <p className="route-error">{error}</p>}
      {review && (
        <p className="route-review">
          <ShieldCheck size={14} />
          {review.domains.join(", ")} → {review.service}:{review.container_port}
          <span>
            {review.imports_routes ? "Moves the existing Caddy site" : "New Caddy route"} ·
            containers unchanged
          </span>
        </p>
      )}
    </div>
  );
}
