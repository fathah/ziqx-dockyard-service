import Button from "./Button";
import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  Check,
  Download,
  HeartPulse,
  Layers3,
  RefreshCw,
  ShieldCheck,
  Terminal,
  X,
} from "lucide-react";
import Accordion from "./Accordion";
import Select from "./Select";
import * as api from "./api";
import type { Project, Service } from "./types";

function explain(error: unknown) {
  const text = String(error);
  const messages: Record<string, string> = {
    HTTP_404:
      "Update the Dockyard service on your VPS, then open this service again.",
    SERVICE_REVIEW_CHANGED:
      "The service, routes or available ports changed. Review again.",
    CONFIGURATION_CHANGED:
      "The project changed. Close this dialog and refresh the project.",
    SERVICE_NETWORK_CHANGED: "The service's network changed. Review again.",
    SERVICE_HAS_STORAGE:
      "This service has writable storage. Choose Controlled restart; the other services stay running.",
    SERVICE_IMAGE_HAS_STORAGE:
      "The new image declares persistent storage. Choose Controlled restart for this service.",
    SERVICE_SEAMLESS_INCOMPATIBLE:
      "This service cannot run two versions safely. Choose Controlled restart or adjust its Compose settings.",
    SERVICE_HEALTHCHECK_REQUIRED:
      "Add a Compose healthcheck to this service before using Seamless updates.",
    SERVICE_EXTRA_PORTS:
      "This service publishes additional ports. Use Controlled restart or keep only its website port.",
    SERVICE_IMAGE_REQUIRED:
      "This service is built locally. Use Edit & deploy to rebuild it.",
    SERVICE_DOMAIN_REQUIRED: "Assign a domain before using Seamless updates.",
    CADDY_UNAVAILABLE:
      "Dockyard cannot read Caddy’s live configuration to verify the traffic switch. Check its private admin connection on the server, then review again.",
    BLUE_GREEN_ROUTE_REVIEW_REQUIRED:
      "The existing Caddy site needs manual review before Dockyard can switch its traffic. Controlled restart keeps the current route.",
    BLUE_GREEN_ROUTE_TARGET_MISMATCH:
      "This service and port do not match the existing website's Caddy connection.",
    PROJECT_BUSY:
      "Wait for the current project operation to finish, then review again.",
    SCOPE_REQUIRED:
      "Enable Compose management in Server details to update services.",
    COMPOSE_ACCESS_REQUIRED:
      "Enable Compose management in Server details first.",
  };
  return (
    Object.entries(messages).find(([code]) => text.includes(code))?.[1] ??
    text.replace(/^Error: /, "")
  );
}

export default function ServiceUpdate({
  project,
  service,
  preview,
  execute,
  onClose,
}: {
  project: Project;
  service: Service;
  preview: boolean;
  execute: (mutation: unknown) => Promise<void>;
  onClose: () => void;
}) {
  const title = useId();
  const dialog = useRef<HTMLDialogElement>(null);
  const sequence = useRef(0);
  const [mode, setMode] = useState<"seamless" | "restart">(
    service.seamless ? "seamless" : "restart",
  );
  const [port, setPort] = useState(
    String(
      project.route_service === service.name
        ? (project.route_port ?? "")
        : (service.container_ports?.[0] ?? ""),
    ),
  );
  const [path, setPath] = useState(project.readiness_path ?? "");
  const [checked, setChecked] = useState<{
    key: string;
    review: api.ServiceUpdateReview;
  }>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const release = project.slots[project.active_slot ?? "blue"]?.id ?? "";
  const data: api.ServiceUpdateInput = {
    expected_release_id: release,
    service: service.name,
    mode,
    ...(mode === "seamless"
      ? { container_port: Number(port), readiness_path: path }
      : {}),
  };
  // Include the service snapshot so a refresh invalidates an old review.
  const key = JSON.stringify([project, service, data]);
  const review = checked?.key === key ? checked.review : undefined;
  const valid =
    !!release &&
    service.updatable === true &&
    (mode === "restart" ||
      (service.seamless === true &&
        Number.isInteger(Number(port)) &&
        Number(port) > 0 &&
        Number(port) <= 65535 &&
        (!path || (path.startsWith("/") && !/[?#\r\n\0]/.test(path)))));
  useEffect(() => {
    const node = dialog.current;
    node?.showModal();
    return () => {
      sequence.current++;
      node?.close();
    };
  }, []);
  useEffect(() => {
    sequence.current++;
    setChecked(undefined);
    setError("");
  }, [key]);

  async function check() {
    const current = ++sequence.current;
    setBusy(true);
    setError("");
    setChecked(undefined);
    try {
      const result = preview
        ? {
            review_sha256: "sample",
            service: service.name,
            mode,
            domains: project.domains,
            container_port: Number(port),
            host_port: 4104,
            imports_routes: !!project.adoption,
            dependencies_unchanged: true,
          }
        : await api.previewServiceUpdate(project.id, data);
      if (current === sequence.current) setChecked({ key, review: result });
    } catch (e) {
      if (current === sequence.current) setError(String(e));
    } finally {
      setBusy(false);
    }
  }
  async function submit() {
    if (!review || preview) return;
    setBusy(true);
    setError("");
    try {
      await execute({
        action: "service_update",
        project: project.id,
        data: { ...data, review_sha256: review.review_sha256 },
      });
      onClose();
    } catch (e) {
      setError(String(e));
      setChecked(undefined);
    } finally {
      setBusy(false);
    }
  }
  return createPortal(
    <dialog
      ref={dialog}
      className="modal service-update-modal"
      aria-labelledby={title}
      onCancel={(e) => {
        e.preventDefault();
        if (!busy) onClose();
      }}
    >
      <div className="modal-heading">
        <div>
          <div className="eyebrow">
            {project.app_id} · {project.environment}
          </div>
          <h2 id={title}>Update {service.name}</h2>
        </div>
        <Button
          type="button"
          className="icon-button"
          aria-label="Close update dialog"
          disabled={busy}
          onClick={onClose}
        >
          <X size={20} />
        </Button>
      </div>
      <div className="wizard-body">
        <p className="service-update-intro">
          Pull the latest image for this service. Your other services, database
          and data volumes stay in place.
        </p>
        {!review && (
          <fieldset className="deployment-strategy" disabled={busy}>
            <legend>How to update</legend>
            <div className="strategy-options">
              <Button
                type="button"
                aria-pressed={mode === "seamless"}
                disabled={!service.seamless || busy}
                className={mode === "seamless" ? "selected" : ""}
                onClick={() => setMode("seamless")}
              >
                <Layers3 size={21} />
                <span>
                  <strong>Seamless updates</strong>
                  <small>Check the new version, then switch visitors</small>
                </span>
              </Button>
              <Button
                type="button"
                aria-pressed={mode === "restart"}
                disabled={busy}
                className={mode === "restart" ? "selected" : ""}
                onClick={() => setMode("restart")}
              >
                <RefreshCw size={21} />
                <span>
                  <strong>Controlled restart</strong>
                  <small>Briefly stops only this service</small>
                </span>
              </Button>
            </div>
          </fieldset>
        )}
        {!review && !service.seamless && service.update_reason && (
          <p className="configuration-note">{service.update_reason}</p>
        )}
        {!review && mode === "seamless" && (
          <>
            <div className="detected-app-route">
              <Check size={19} />
              <div>
                <strong>
                  {service.name} <span>· port {port || "not declared"}</span>
                </strong>
                <small>Detected from your saved Compose file</small>
              </div>
            </div>
            <Accordion
              title="App connection & health check"
              icon={HeartPulse}
              className="route-detection-options"
              defaultOpen={!port}
            >
              <p>
                The container port is where this app listens. Compose
                healthchecks run automatically.
              </p>
              <div className="form-grid">
                {(service.container_ports?.length ?? 0) > 1 ? (
                  <Select
                    label="App port inside container"
                    value={port}
                    onValueChange={setPort}
                    disabled={busy}
                    options={service.container_ports!.map((value) => ({
                      value: String(value),
                      label: String(value),
                    }))}
                  />
                ) : (
                  <label>
                    App port inside container
                    <input
                      type="number"
                      min={1}
                      max={65535}
                      value={port}
                      disabled={busy}
                      onChange={(e) => setPort(e.target.value)}
                    />
                  </label>
                )}
                <label>
                  HTTP health path <small>(optional)</small>
                  <input
                    value={path}
                    disabled={busy}
                    onChange={(e) => setPath(e.target.value)}
                    placeholder="/health"
                  />
                </label>
              </div>
            </Accordion>
          </>
        )}
        {error && (
          <p className="alert error" role="alert">
            {explain(error)}
          </p>
        )}
        {error.includes("CADDY_UNAVAILABLE") && (
          <Accordion
            title="Check Caddy on the server"
            icon={Terminal}
            className="caddy-connection-help"
          >
            <p>Run these checks in the VPS terminal:</p>
            <pre>
              <code>{`systemctl is-active caddy
systemctl show dockyard -p User -p CapabilityBoundingSet
test -S /run/caddy/admin.sock && echo socket-present || echo socket-missing
curl --max-time 5 --unix-socket /run/caddy/admin.sock -sS -o /dev/null -w 'Caddy HTTP %{http_code}\\n' http://127.0.0.1/config/`}</code>
            </pre>
            <p>
              Expect HTTP 200. A socket file can exist without Caddy listening
              on it. If the connection fails, check Caddy’s active admin endpoint
              and the private socket setting in its Caddyfile.
            </p>
            <p>
              If Caddy is active and the socket exists, older root installations
              with an empty capability bounding set need{" "}
              <code>CAP_DAC_OVERRIDE</code>
              restored in Dockyard’s systemd service. Updating the binaries
              alone does not update those service settings.
            </p>
            <p>
              If Caddy is inactive or its socket is missing, check its service
              log and private admin configuration. Keep the admin connection
              private. Seamless updates require a verified Caddy connection.
            </p>
          </Accordion>
        )}
        {review && (
          <div className="service-update-plan">
            <h3>
              <ShieldCheck size={19} />{" "}
              {mode === "seamless" ? "Seamless updates" : "Controlled restart"}
            </h3>
            <ol>
              <li>
                Pull this service's image. Skip deployment if it is already
                current.
              </li>
              {mode === "seamless" ? (
                <>
                  <li>Start a separate app version and check its health.</li>
                  <li>
                    Switch {review.domains.join(" · ")} to the new version.
                  </li>
                  <li>Let requests finish, then stop the previous app.</li>
                </>
              ) : (
                <li>
                  Recreate only {service.name} with the new image, then check
                  that it starts.
                </li>
              )}
            </ol>
            <p className="configuration-note">
              Other services keep running. Compose files and .env are unchanged.
              {review.imports_routes &&
                " This website's reviewed Caddy routes will be managed by Dockyard."}
            </p>
          </div>
        )}
        {preview && (
          <p className="configuration-note">
            Sample preview · no server changes.
          </p>
        )}
      </div>
      <div className="modal-footer">
        <Button
          type="button"
          className="button"
          disabled={busy}
          onClick={onClose}
        >
          Cancel
        </Button>
        {review && (
          <Button
            type="button"
            className="button"
            disabled={busy}
            onClick={() => {
              sequence.current++;
              setChecked(undefined);
              setError("");
            }}
          >
            Change settings
          </Button>
        )}
        {review ? (
          <Button
            type="button"
            className="button primary"
            disabled={busy || preview}
            onClick={() => void submit()}
          >
            <Download size={17} />
            {busy ? "Submitting…" : "Pull & update"}
          </Button>
        ) : (
          <Button
            type="button"
            className="button primary"
            disabled={busy || !valid}
            onClick={() => void check()}
          >
            <ShieldCheck size={17} />
            {busy ? "Reviewing…" : "Review update"}
          </Button>
        )}
      </div>
    </dialog>,
    document.body,
  );
}
