import Button from "./Button";
import { useEffect, useMemo, useRef, useState } from "react";
import {
  Check,
  HeartPulse,
  Layers3,
  ListChecks,
  Settings2,
  ShieldCheck,
  RefreshCw,
} from "lucide-react";
import Accordion from "./Accordion";
import Select from "./Select";
import * as api from "./api";
import type { Project } from "./types";
import { detectComposeRoutes, suggestComposeRoute } from "./composeRoute";

function explain(error: unknown) {
  const text = String(error);
  const messages: Record<string, string> = {
    BLUE_GREEN_SOURCE_HAS_STORAGE:
      "The current stack contains persistent storage. Keep its database/storage services in a separate Compose project before converting the application stack; Dockyard will not stop or duplicate them automatically.",
    HTTP_404:
      "Update the VPS service to 0.6.0 in Server details, then review again.",
    COMPOSE_BLUE_GREEN_INCOMPATIBLE:
      "This draft has shared resources or fixed names. Remove container_name, fixed network names, extra published ports, and volumes from the application stack. Keep databases and persistent storage in a separate service. Your live stack is unchanged.",
    COMPOSE_BLUE_GREEN_HEALTHCHECK_REQUIRED:
      "Add a healthcheck to every service in the draft before enabling seamless updates.",
    BLUE_GREEN_ROUTE_REVIEW_REQUIRED:
      "Automatic cutover supports plain reverse_proxy sites in the main Caddyfile. Custom handlers or imported routes need to be simplified or reviewed on the VPS first. The current routes are unchanged.",
    BLUE_GREEN_ROUTE_TARGET_MISMATCH:
      "The selected service and container port do not match the existing Caddy upstream. Choose the service currently serving these domains.",
    BLUE_GREEN_REVIEW_CHANGED:
      "The project, routes, or available ports changed. Review the plan again.",
    CONFIGURATION_CHANGED:
      "A new release changed the project. Reload its saved files before reviewing.",
    BLUE_GREEN_RUNNING_SINGLE_REQUIRED:
      "Start this single-instance production project before enabling seamless updates.",
    COMPOSE_BLUE_GREEN_ROUTE_REQUIRED:
      "Add a domain before enabling seamless updates. Dockyard needs a route to switch traffic between slots.",
    COMPOSE_ROUTE_SERVICE_MISSING:
      "The web service name must match a service in the Compose draft.",
    COMPOSE_VALIDATION_FAILED:
      "Docker could not validate this draft. Check its syntax, .env variables, and file paths.",
    SCOPE_REQUIRED:
      "Enable Compose management in Server details to grant the required deployment and routing permissions.",
    COMPOSE_ACCESS_REQUIRED:
      "Enable Compose management in Server details first.",
    PROJECT_BUSY:
      "Wait for the current project operation to finish, then review again.",
  };
  return (
    Object.entries(messages).find(([code]) => text.includes(code))?.[1] ??
    text.replace(/^Error: /, "")
  );
}
export default function BlueGreenDeployment({
  project,
  compose,
  dotenv,
  release,
  preview,
  execute,
  onAccepted,
}: {
  project: Project;
  compose: string;
  dotenv: string;
  release: string;
  preview: boolean;
  execute: (mutation: unknown) => Promise<void>;
  onAccepted: () => void;
}) {
  const detection = useMemo(
    () => detectComposeRoutes(compose, dotenv),
    [compose, dotenv],
  );
  const suggested = suggestComposeRoute(
    detection,
    project.route_service,
    project.route_port,
  );
  const sourceKey = JSON.stringify([compose, dotenv]);
  const [selection, setSelection] = useState<{
    sourceKey: string;
    service: string;
    port: string;
  }>();
  const { service, port } =
    selection?.sourceKey === sourceKey ? selection : suggested;
  const selectedService = detection.services.find((s) => s.name === service);
  function selectService(name: string) {
    const next = suggestComposeRoute(detection, name);
    setSelection({ sourceKey, service: name, port: next.port });
  }
  function selectPort(value: string) {
    setSelection({ sourceKey, service, port: value });
  }
  const [path, setPath] = useState(project.readiness_path ?? "");
  const [checked, setChecked] = useState<{
    key: string;
    review: api.BlueGreenReview;
  }>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    setChecked(undefined);
    setError("");
  }, [compose, dotenv, release, service, port, path]);
  const valid =
    !!compose.trim() &&
    !!selectedService &&
    !detection.issue &&
    Number.isInteger(Number(port)) &&
    Number(port) > 0 &&
    Number(port) <= 65535;
  const data: api.BlueGreenInput = {
    expected_release_id: release,
    compose_yaml: compose,
    env_file: dotenv,
    route_service: service,
    route_port: Number(port),
    readiness_path: path,
  };
  const reviewKey = JSON.stringify([project.id, data]);
  const review = checked?.key === reviewKey ? checked.review : undefined;
  const checkSequence = useRef(0);
  useEffect(
    () => () => {
      checkSequence.current++;
    },
    [],
  );
  async function check() {
    const sequence = ++checkSequence.current;
    setBusy(true);
    setError("");
    setChecked(undefined);
    try {
      const result = preview
        ? {
            review_sha256: "sample",
            blue_port: 4104,
            green_port: 4105,
            domains: project.domains,
            route_service: service,
            route_port: Number(port),
            imports_routes: !!project.adoption,
          }
        : await api.previewBlueGreen(project.id, data);
      if (sequence === checkSequence.current)
        setChecked({ key: reviewKey, review: result });
    } catch (e) {
      setError(explain(e));
    } finally {
      setBusy(false);
    }
  }
  async function enable() {
    if (!review || preview) return;
    setBusy(true);
    setError("");
    try {
      await execute({
        action: "blue_green",
        project: project.id,
        data: { ...data, review_sha256: review.review_sha256 },
      });
      onAccepted();
    } catch (e) {
      setError(explain(e));
      setChecked(undefined);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="blue-green-review">
      <div className="section-heading">
        <h3>
          <Layers3 size={19} /> Seamless updates
        </h3>
      </div>
      <p className="seamless-subtitle">
        Keep your app online while updates are checked and switched into place.
      </p>
      {detection.issue && <p className="alert pending">{detection.issue}</p>}
      {valid && (
        <div className="detected-app-route">
          <Check size={19} aria-hidden="true" />
          <div>
            <strong>
              {service} <span>· port {port}</span>
            </strong>
            <small>
              {selection?.sourceKey === sourceKey
                ? "Your selected app connection"
                : project.route_service === service &&
                    project.route_port === Number(port)
                  ? "Using your existing website connection"
                  : "Detected from your Compose file"}
            </small>
          </div>
        </div>
      )}
      <Accordion
        className="route-detection-options"
        defaultOpen={!valid}
        key={valid ? "detected" : "choose"}
        icon={Settings2}
        title={
          valid ? "Change app connection" : "Choose the app for your domain"
        }
      >
        <p>
          Your domain will send visitors to this service. The port is where the
          app listens inside its container.
        </p>
        <div className="form-grid">
          <Select
            label="App service"
            value={service}
            onValueChange={selectService}
            disabled={busy}
            placeholder="Select a service from Compose"
            options={detection.services.map((s) => ({
              value: s.name,
              label: `${s.name} · ${s.ports.length ? s.ports.join(", ") : "port not declared"}`,
            }))}
          />
          {selectedService &&
          selectedService.ports.length > 1 &&
          !selectedService.unresolved ? (
            <Select
              label="App port inside container"
              value={port}
              onValueChange={selectPort}
              disabled={busy}
              placeholder="Select the app's HTTP port"
              options={selectedService.ports.map((p) => ({
                value: String(p),
                label: String(p),
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
                onChange={(e) => selectPort(e.target.value)}
                disabled={busy || !selectedService}
                placeholder="e.g. 3000"
              />
              {selectedService &&
                (!selectedService.ports.length ||
                  selectedService.unresolved) && (
                  <small>
                    Not fully detected. Add ports or expose to Compose, or enter
                    the app's listening port.
                  </small>
                )}
            </label>
          )}
        </div>
      </Accordion>
      <Accordion
        className="route-detection-options"
        icon={HeartPulse}
        title={
          <>
            Advanced health check{" "}
            {path && <span className="accordion-status">Configured</span>}
          </>
        }
      >
        <p>
          Compose healthchecks are used automatically. Optionally check an HTTP
          endpoint before sending visitors to the new version.
        </p>
        <label>
          Health check path <small>(optional)</small>
          <input
            value={path}
            onChange={(e) => setPath(e.target.value)}
            disabled={busy}
            placeholder="/health"
          />
        </label>
      </Accordion>
      {error && (
        <p className="alert error" role="alert">
          {error}
        </p>
      )}
      {!review ? (
        <Button
          type="button"
          className="button primary"
          disabled={busy || !valid}
          onClick={() => void check()}
        >
          <ShieldCheck size={18} />
          {busy ? "Checking compatibility…" : "Review seamless updates"}
        </Button>
      ) : (
        <div className="blue-green-plan">
          <h3>
            <Check size={19} /> Ready to enable seamless updates
          </h3>
          <dl>
            <dt>Traffic</dt>
            <dd>{review.domains.join(" · ")}</dd>
            <dt>App connection</dt>
            <dd>
              {review.route_service}:{review.route_port}
            </dd>
            <dt>Server ports</dt>
            <dd>
              {review.blue_port} and {review.green_port} · assigned
              automatically
            </dd>
          </dl>
          <p>
            Your current version keeps serving visitors until the update is
            healthy. If checks fail during the switch, the original route is
            restored.
          </p>
          <Accordion
            className="route-detection-options"
            title="Deployment details"
            icon={ListChecks}
          >
            {review.imports_routes && (
              <p>
                The reviewed Caddy sites move into Dockyard’s managed routes.
                Original route files and project metadata are retained in the
                recovery journal.
              </p>
            )}
            <p>
              New deployment history starts with this conversion. Previous
              release files remain on the VPS.
            </p>
          </Accordion>
          <div className="action-row">
            <Button
              type="button"
              className="button primary"
              disabled={busy || preview}
              onClick={() => void enable()}
            >
              <Layers3 size={18} />
              {busy ? "Submitting…" : "Enable seamless updates"}
            </Button>
            <Button
              type="button"
              className="button"
              disabled={busy}
              onClick={() => void check()}
            >
              <RefreshCw size={17} />
              Check again
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
