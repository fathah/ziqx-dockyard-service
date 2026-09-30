import { useEffect, useState } from "react";
import { Check, Layers3, ShieldCheck, RefreshCw } from "lucide-react";
import * as api from "./api";
import type { Project } from "./types";

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
      "Add a healthcheck to every service in the draft before enabling blue–green.",
    BLUE_GREEN_ROUTE_REVIEW_REQUIRED:
      "Automatic cutover supports plain reverse_proxy sites in the main Caddyfile. Custom handlers or imported routes need to be simplified or reviewed on the VPS first. The current routes are unchanged.",
    BLUE_GREEN_ROUTE_TARGET_MISMATCH:
      "The selected service and container port do not match the existing Caddy upstream. Choose the service currently serving these domains.",
    BLUE_GREEN_REVIEW_CHANGED:
      "The project, routes, or available ports changed. Review the plan again.",
    CONFIGURATION_CHANGED:
      "A new release changed the project. Reload its saved files before reviewing.",
    BLUE_GREEN_RUNNING_SINGLE_REQUIRED:
      "Start this single-instance production project before enabling blue–green.",
    COMPOSE_BLUE_GREEN_ROUTE_REQUIRED:
      "Add a domain before enabling blue–green. Dockyard needs a route to switch traffic between slots.",
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
  const [service, setService] = useState(project.route_service ?? "web");
  const [port, setPort] = useState(String(project.route_port || 80));
  const [path, setPath] = useState(project.readiness_path ?? "");
  const [review, setReview] = useState<api.BlueGreenReview>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    setReview(undefined);
    setError("");
  }, [compose, dotenv, release, service, port, path]);
  const valid =
    !!compose.trim() &&
    !!service.trim() &&
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
  async function check() {
    setBusy(true);
    setError("");
    setReview(undefined);
    try {
      setReview(
        preview
          ? {
              review_sha256: "sample",
              blue_port: 4104,
              green_port: 4105,
              domains: project.domains,
              route_service: service,
              route_port: Number(port),
              imports_routes: !!project.adoption,
            }
          : await api.previewBlueGreen(project.id, data),
      );
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
      setReview(undefined);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="blue-green-review">
      <div className="section-heading">
        <h3>
          <Layers3 size={19} /> Blue–green settings
        </h3>
      </div>
      <p>
        Use the Compose draft above for the new slots. The current instance
        stays live until the green slot is healthy.
      </p>
      <div className="form-grid">
        <label>
          Web service
          <input
            value={service}
            onChange={(e) => setService(e.target.value)}
            disabled={busy}
            placeholder="web"
          />
        </label>
        <label>
          Container port
          <input
            type="number"
            min={1}
            max={65535}
            value={port}
            onChange={(e) => setPort(e.target.value)}
            disabled={busy}
          />
        </label>
        <label>
          Readiness URL path <small>(optional)</small>
          <input
            value={path}
            onChange={(e) => setPath(e.target.value)}
            disabled={busy}
            placeholder="/health"
          />
        </label>
      </div>
      {error && (
        <p className="alert error" role="alert">
          {error}
        </p>
      )}
      {!review ? (
        <button
          type="button"
          className="button primary"
          disabled={busy || !valid}
          onClick={() => void check()}
        >
          <ShieldCheck size={18} />
          {busy ? "Checking compatibility…" : "Review blue–green"}
        </button>
      ) : (
        <div className="blue-green-plan">
          <h3>
            <Check size={19} /> Ready to enable blue–green
          </h3>
          <dl>
            <dt>Traffic</dt>
            <dd>{review.domains.join(" · ")}</dd>
            <dt>Web service</dt>
            <dd>
              {review.route_service}:{review.route_port}
            </dd>
            <dt>Slot ports</dt>
            <dd>
              Blue :{review.blue_port} · Green :{review.green_port}
            </dd>
          </dl>
          <p>
            Start green → verify health → switch traffic → drain and stop the
            original instance. If readiness fails before the switch, traffic
            stays on the original. If it fails during draining, Dockyard
            restores the original route.
          </p>
          {review.imports_routes && (
            <p>
              The reviewed Caddy sites move into Dockyard’s managed routes.
              Original route files and project metadata are retained in the
              recovery journal.
            </p>
          )}
          <p>
            New deployment history starts with this conversion. Previous release
            files remain on the VPS.
          </p>
          <div className="action-row">
            <button
              type="button"
              className="button primary"
              disabled={busy || preview}
              onClick={() => void enable()}
            >
              <Layers3 size={18} />
              {busy ? "Submitting…" : "Enable blue–green"}
            </button>
            <button
              type="button"
              className="button"
              disabled={busy}
              onClick={() => void check()}
            >
              <RefreshCw size={17} />
              Check again
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
