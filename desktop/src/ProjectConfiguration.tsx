import Button from "./Button";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import {
  ArrowUpRight,
  FileCode2,
  Fingerprint,
  RefreshCw,
  Settings2,
  AlignLeft,
  Save,
  Upload,
} from "lucide-react";
import toast from "react-hot-toast";
import * as api from "./api";
import Help from "./Help";
import CodeEditor, { type CodeEditorHandle } from "./CodeEditor";
import { composeForEditor, lintCompose } from "./composeLint";
import { addHealthcheck, suggestHealthcheck } from "./composeHealthcheck";
import SecretGenerator from "./SecretGenerator";
import Select from "./Select";
import { detectComposeRoutes, suggestComposeRoute } from "./composeRoute";
import type { Project } from "./types";

export type HealthcheckRequest = {
  service: string;
  image: string;
  port?: number;
  path: string;
};

type Configuration = {
  release_id: string;
  compose_yaml: string;
  env_file: string;
  draft?: boolean;
};
// Compose loads without Touch ID; the server's .env is fetched separately.
type ComposeConfiguration = Omit<Configuration, "env_file"> & {
  env_file?: string;
};
// Docker's explanation, if the server sent one (lines after the error code).
function detailOf(error: unknown): string {
  return String(error).split("\n").slice(1).join("\n").trim();
}

function message(error: unknown): string {
  const text = String(error).split("\n")[0];
  if (/HTTP_404/.test(text))
    return "Update the Dockyard service on your VPS in Server details → Updates, then try again.";
  if (/CONFIGURATION_CHANGED/.test(text))
    return "Another release changed this project. Copy your edits, reload the current files, then apply your changes again.";
  if (/SCOPE_REQUIRED|COMPOSE_ACCESS_REQUIRED/.test(text))
    return "Enable Compose management in Server details to edit this project.";
  if (
    /COMPOSE_VALIDATION_FAILED|COMPOSE_RESOLUTION_FAILED|COMPOSE_INVALID/.test(
      text,
    )
  )
    return "Compose validation failed. Check YAML, required .env values, and referenced files on the VPS. Your edits are still here.";
  if (/COMPOSE_ROUTE_SERVICE_MISSING/.test(text))
    return "The service that should receive your domain's traffic isn't in this Compose file. Pick the right one below, then deploy.";
  if (/COMPOSE_ROUTE_INVALID/.test(text))
    return "Choose the service and container port that should receive your domain's traffic.";
  if (/PROJECT_BUSY/.test(text))
    return "A project operation is still running. Wait for it to finish, then deploy again.";
  if (
    /COMPOSE_CONFIG_DIVERGED|ENVIRONMENT_IDENTITY_UNKNOWN|CONFIGURATION_UNAVAILABLE/.test(
      text,
    )
  )
    return "The saved configuration could not be verified. Restore the release files on the VPS before editing.";
  return text.replace(/^Error: /, "");
}

export default function ProjectConfiguration({
  project,
  preview,
  execute,
  onDirtyChange,
  healthcheck,
  onHealthcheckHandled,
}: {
  project: Project;
  preview: boolean;
  onDirtyChange: (dirty: boolean) => void;
  execute: (mutation: unknown) => Promise<void>;
  healthcheck?: HealthcheckRequest;
  onHealthcheckHandled?: () => void;
}) {
  const initial: Configuration | undefined = preview
    ? {
        release_id: "preview",
        compose_yaml:
          "services:\n  web:\n    image: nginx:alpine\n    environment:\n      APP_URL: ${APP_URL}\n",
        env_file: "APP_URL=https://example.com\n",
      }
    : undefined; // Always ask the server: a never-deployed project may have a saved draft.
  const [saved, setSaved] = useState<Configuration | undefined>(initial);
  const [compose, setCompose] = useState(initial?.compose_yaml ?? "");
  const [dotenv, setDotenv] = useState(initial?.env_file ?? "");
  const [file, setFile] = useState<"compose" | "env">("compose");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [errorDetail, setErrorDetail] = useState("");
  // The editor holds a saved draft that is not (yet) the deployed release.
  const [isDraft, setIsDraft] = useState(false);
  // A saved release's .env stays hidden until Touch ID (2-minute reuse).
  const [envLoaded, setEnvLoaded] = useState(!!initial);
  const composeEditor = useRef<CodeEditorHandle>(null);
  const isJson = compose.trimStart().startsWith("{");
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const dirty =
    !!saved &&
    (compose !== saved.compose_yaml ||
      (envLoaded && dotenv !== saved.env_file));
  useEffect(() => {
    onDirtyChange(dirty);
    return () => onDirtyChange(false);
  }, [dirty, onDirtyChange]);
  // "Add healthcheck" from a service: load the saved files if needed, insert
  // the probe into the editor and leave it unsaved for review and deploy.
  useEffect(() => {
    if (!healthcheck) return;
    onHealthcheckHandled?.();
    const probe = suggestHealthcheck(
      healthcheck.image,
      healthcheck.port,
      healthcheck.path,
    );
    if (!probe) {
      setError(
        `Set ${healthcheck.service}'s container port first, or add a healthcheck by hand.`,
      );
      return;
    }
    const apply = (source: string) => {
      try {
        setCompose(addHealthcheck(source, healthcheck.service, probe));
        setFile("compose");
        setError("");
        setErrorDetail("");
        setNotice(
          `Healthcheck added to ${healthcheck.service} — review it, then Deploy.`,
        );
      } catch (e) {
        setError(message(e));
      }
    };
    if (saved) {
      apply(compose);
      return;
    }
    setBusy(true);
    setError("");
    setErrorDetail("");
    fetchCompose()
      .then((source) => source !== undefined && apply(source))
      .catch((e) => alive.current && setError(message(e)))
      .finally(() => alive.current && setBusy(false));
    // Runs once per request; saved/compose are read at that moment.
  }, [healthcheck]);
  const bytes = (value: string) => new TextEncoder().encode(value).length;
  // The project's web service must exist in the file being deployed.
  const routable =
    project.mode === "compose" &&
    project.domains.length > 0 &&
    !project.zerodowntime &&
    !project.published_route;
  const detection = useMemo(
    () => detectComposeRoutes(compose, dotenv),
    [compose, dotenv],
  );
  const routeMissing =
    routable &&
    !!saved &&
    detection.services.length > 0 &&
    !detection.services.some((s) => s.name === project.route_service);
  const [routeFix, setRouteFix] = useState({ service: "", port: "" });
  useEffect(() => {
    if (!routeMissing) return;
    setRouteFix((current) => {
      if (detection.services.some((s) => s.name === current.service))
        return current;
      const suggestion = suggestComposeRoute(detection);
      return {
        service: suggestion.service,
        port: suggestion.port ? String(suggestion.port) : "",
      };
    });
  }, [routeMissing, detection]);
  const routePortValid =
    Number.isInteger(Number(routeFix.port)) &&
    Number(routeFix.port) > 0 &&
    Number(routeFix.port) <= 65535;
  async function fetchCompose(): Promise<string | undefined> {
    const data = await api.read<ComposeConfiguration>({
      kind: "configuration",
      project: project.id,
    });
    if (!alive.current) return undefined;
    const source = composeForEditor(data.compose_yaml);
    setSaved({ ...data, compose_yaml: source, env_file: "" });
    setCompose(source);
    setDotenv("");
    setIsDraft(!!data.draft);
    // A draft's .env is on the server too; only a blank new project starts empty.
    setEnvLoaded(!data.release_id && !data.draft);
    return source;
  }
  async function load() {
    if (
      dirty &&
      !window.confirm(
        "Discard your unsaved edits and reload the server configuration?",
      )
    )
      return;
    setBusy(true);
    setError("");
    setErrorDetail("");
    try {
      await fetchCompose();
    } catch (e) {
      if (alive.current) setError(message(e));
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  // Open the Compose file as soon as the tab is shown.
  useEffect(() => {
    if (!saved && !healthcheck && !preview) void load();
    // Mount only.
  }, []);
  async function fetchEnv(): Promise<string> {
    if (envLoaded || !saved) return dotenv;
    const data = await api.readEnv(project.id);
    if (data.release_id !== saved.release_id)
      throw new Error("Files changed on the server. Reload and try again.");
    if (alive.current) {
      setSaved({ ...saved, env_file: data.env_file });
      setDotenv(data.env_file);
      setEnvLoaded(true);
    }
    return data.env_file;
  }
  async function unlockEnv() {
    setBusy(true);
    setError("");
    setErrorDetail("");
    try {
      await fetchEnv();
    } catch (e) {
      if (alive.current) setError(message(e));
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  async function save() {
    if (!saved) return;
    setBusy(true);
    setError("");
    setErrorDetail("");
    try {
      await api.saveDraft(project.id, {
        compose_yaml: compose,
        ...(envLoaded ? { env_file: dotenv } : {}),
      });
      if (!alive.current) return;
      setSaved({
        ...saved,
        compose_yaml: compose,
        env_file: envLoaded ? dotenv : saved.env_file,
      });
      setIsDraft(true);
      toast.success("Saved · not deployed yet");
    } catch (e) {
      if (alive.current) {
        setError(message(e));
        setErrorDetail(detailOf(e));
      }
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  async function deploy(e: FormEvent) {
    e.preventDefault();
    if (!saved) return;
    if (lintCompose(compose).some((p) => p.severity === "error")) {
      setFile("compose");
      setError("Fix the highlighted Compose syntax errors before deploying.");
      return;
    }
    if (routeMissing && (!routeFix.service || !routePortValid)) {
      setFile("compose");
      setError(
        "Choose the service and port that should receive your domain's traffic.",
      );
      return;
    }
    setBusy(true);
    setError("");
    setErrorDetail("");
    try {
      // Deploy needs the .env; its Touch ID also covers the deploy prompt.
      const env = await fetchEnv();
      await execute({
        action: "deploy",
        project: project.id,
        data: {
          environment: project.environment,
          compose_yaml: compose,
          env_file: env,
          ...(routeMissing
            ? {
                route_service: routeFix.service,
                route_port: Number(routeFix.port),
              }
            : {}),
          expected_release_id: saved.release_id,
        },
      });
      if (alive.current) {
        // Deploy saved these files as the draft; keep them open.
        setSaved({ ...saved, compose_yaml: compose, env_file: env });
        setDotenv(env);
        setEnvLoaded(true);
        setIsDraft(true);
      }
    } catch (e) {
      if (alive.current) {
        setError(message(e));
        setErrorDetail(detailOf(e));
      }
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  return (
    <section className="panel project-configuration">
      <div className="configuration-heading">
        <h2>
          Project files{" "}
          <Help label="Project files">
            This is Dockyard's saved release; edits made directly on the VPS
            aren't imported. Deploy validates both files and saves a new
            release.
            {project.adoption &&
              project.adoption.source_name !== project.id &&
              ` Original files in /docker/${project.adoption.source_name} are kept; build contexts and relative paths still use that folder.`}
          </Help>
        </h2>
      </div>
      <p className="configuration-subtitle">
        {project.environment} · /docker/{project.id}/compose.yml and .env
      </p>
      {error && (
        <div className="alert error configuration-error" role="alert">
          <span>{error}</span>
          {errorDetail && <pre>{errorDetail}</pre>}
        </div>
      )}
      {notice && dirty && (
        <p className="alert pending" role="status">
          {notice}
        </p>
      )}
      {!saved ? (
        <div className="configuration-unlock">
          {busy ? (
            <div
              className="configuration-loading"
              role="status"
              aria-label="Loading project files"
            >
              <span className="skeleton-bar">
                <span className="skeleton-fill" />
              </span>
              <span className="skeleton-bar">
                <span className="skeleton-fill" />
              </span>
              <span className="skeleton-bar">
                <span className="skeleton-fill" />
              </span>
            </div>
          ) : (
            <>
              <FileCode2 size={32} />
              <Button
                type="button"
                className="button primary"
                disabled={preview}
                onClick={load}
              >
                <RefreshCw size={18} /> Open files
              </Button>
            </>
          )}
        </div>
      ) : (
        <form onSubmit={deploy}>
          <div className="configuration-toolbar">
            <div className="tabs" role="tablist" aria-label="Project files">
              <Button
                type="button"
                role="tab"
                aria-selected={file === "compose"}
                className={file === "compose" ? "selected" : ""}
                onClick={() => setFile("compose")}
              >
                <FileCode2 size={18} aria-hidden="true" />
                compose.yml
              </Button>
              <Button
                type="button"
                role="tab"
                aria-selected={file === "env"}
                className={file === "env" ? "selected" : ""}
                onClick={() => setFile("env")}
              >
                <Settings2 size={18} aria-hidden="true" />
                .env
              </Button>
            </div>
            <div
              className="configuration-actions"
              role="group"
              aria-label="File actions"
            >
              <Button
                type="button"
                className="button small"
                disabled={busy || preview}
                onClick={async () => {
                  try {
                    const value =
                      file === "compose"
                        ? await api.importCompose()
                        : await api.importEnv();
                    if (value !== null && alive.current) {
                      if (file === "compose")
                        setCompose(composeForEditor(value));
                      else {
                        setDotenv(value);
                        setEnvLoaded(true);
                      }
                    }
                  } catch (e) {
                    setError(message(e));
                  }
                }}
              >
                <Upload size={15} />{" "}
                {file === "compose" ? "Import Compose" : "Import .env"}
              </Button>
              {file === "compose" && (
                <>
                  <Button
                    type="button"
                    className="button small"
                    disabled={busy || preview}
                    onClick={() => composeEditor.current?.format()}
                  >
                    <AlignLeft size={15} aria-hidden="true" /> Format
                  </Button>
                  {isJson && (
                    <Button
                      type="button"
                      className="button small"
                      disabled={busy || preview}
                      onClick={() => composeEditor.current?.format(true)}
                    >
                      Convert to YAML
                    </Button>
                  )}
                </>
              )}
              {saved.release_id && (
                <Button
                  type="button"
                  className="button small"
                  disabled={busy || preview}
                  onClick={load}
                >
                  <RefreshCw size={15} aria-hidden="true" /> Reload
                </Button>
              )}
            </div>
          </div>
          <div hidden={file !== "compose"}>
            <CodeEditor
              ref={composeEditor}
              showToolbar={false}
              value={compose}
              onChange={setCompose}
              readOnly={busy || preview}
              active={file === "compose"}
            />
          </div>
          <div hidden={file !== "env" || envLoaded} className="env-locked">
            <Fingerprint size={28} aria-hidden="true" />
            <Button
              type="button"
              className="button primary"
              disabled={busy || preview}
              onClick={unlockEnv}
            >
              Show .env
            </Button>
          </div>
          <div hidden={file !== "env" || !envLoaded}>
            <CodeEditor
              showToolbar={false}
              kind="env"
              value={dotenv}
              onChange={setDotenv}
              readOnly={busy || preview}
              active={file === "env"}
            />
          </div>
          <div className="editor-meta">
            <span>
              {file === "compose" ? (isJson ? "JSON" : "YAML") : ".env"} ·{" "}
              {bytes(file === "compose" ? compose : dotenv).toLocaleString()} /
              65,536 bytes
            </span>
            <span>
              {preview
                ? "Sample files · read-only"
                : dirty
                  ? "Unsaved changes"
                  : isDraft
                    ? "Saved · not deployed"
                    : saved.release_id
                      ? "Deployed"
                      : "New project"}
            </span>
          </div>
          {file === "env" && envLoaded && (
            <p className="configuration-note">
              An empty .env clears saved values.
            </p>
          )}
          {file === "env" && envLoaded && <SecretGenerator />}
          {routeMissing && (
            <div className="route-fix" role="group" aria-label="Domain traffic">
              <span>
                <strong>{project.domains[0]}</strong> sends traffic to{" "}
                <code>{project.route_service || "no service"}</code>, which
                isn't in this file. Send it to
              </span>
              <Select
                label="Service"
                value={routeFix.service}
                options={detection.services.map((s) => ({
                  value: s.name,
                  label: s.name,
                }))}
                onValueChange={(service) => {
                  const ports =
                    detection.services.find((s) => s.name === service)?.ports ??
                    [];
                  setRouteFix({
                    service,
                    port: ports[0] ? String(ports[0]) : routeFix.port,
                  });
                }}
              />
              <label className="route-fix-port">
                <span>port</span>
                <input
                  type="number"
                  min={1}
                  max={65535}
                  placeholder="3000"
                  value={routeFix.port}
                  onChange={(e) =>
                    setRouteFix({ ...routeFix, port: e.target.value })
                  }
                />
              </label>
            </div>
          )}
          <div className="configuration-footer">
            <span>
              {project.zerodowntime
                ? "Health checks before traffic switches."
                : "Restarts all services. To update one, use Services → Pull & update."}
            </span>
            <div className="configuration-buttons">
              <Button
                type="button"
                className="button"
                disabled={
                  preview ||
                  busy ||
                  !dirty ||
                  bytes(compose) > 65536 ||
                  bytes(dotenv) > 65536
                }
                title="Save the Compose file and .env on the VPS without deploying"
                onClick={() => void save()}
              >
                <Save size={15} /> Save
              </Button>
              <Button
                type="submit"
                className="button primary"
                disabled={
                  preview ||
                  busy ||
                  !compose.trim() ||
                  bytes(compose) > 65536 ||
                  bytes(dotenv) > 65536
                }
              >
                <ArrowUpRight size={15} /> {busy ? "Working…" : "Deploy"}
              </Button>
            </div>
          </div>
        </form>
      )}
    </section>
  );
}
