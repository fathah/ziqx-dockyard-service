import { useEffect, useRef, useState, type FormEvent } from "react";
import {
  ArrowUpRight,
  FileCode2,
  Layers3,
  Box,
  Fingerprint,
  RefreshCw,
  Settings2,
  Upload,
} from "lucide-react";
import * as api from "./api";
import Help from "./Help";
import BlueGreenDeployment from "./BlueGreenDeployment";
import CodeEditor from "./CodeEditor";
import { lintCompose } from "./composeLint";
import type { Project } from "./types";

type Configuration = {
  release_id: string;
  compose_yaml: string;
  env_file: string;
};
function message(error: unknown): string {
  const text = String(error);
  if (/HTTP_404/.test(text))
    return "Update Dockyard on your VPS to 0.5.0 or later in Server details, then try again.";
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
}: {
  project: Project;
  preview: boolean;
  onDirtyChange: (dirty: boolean) => void;
  execute: (mutation: unknown) => Promise<void>;
}) {
  const sample: Configuration | undefined = preview
    ? {
        release_id: "preview",
        compose_yaml:
          "services:\n  web:\n    image: nginx:alpine\n    environment:\n      APP_URL: ${APP_URL}\n",
        env_file: "APP_URL=https://example.com\n",
      }
    : undefined;
  const [saved, setSaved] = useState<Configuration | undefined>(sample);
  const [compose, setCompose] = useState(sample?.compose_yaml ?? "");
  const [dotenv, setDotenv] = useState(sample?.env_file ?? "");
  const [file, setFile] = useState<"compose" | "env">("compose");
  const [blueGreen, setBlueGreen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const dirty =
    !!saved && (compose !== saved.compose_yaml || dotenv !== saved.env_file);
  useEffect(() => {
    onDirtyChange(dirty);
    return () => onDirtyChange(false);
  }, [dirty, onDirtyChange]);
  const bytes = (value: string) => new TextEncoder().encode(value).length;
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
    try {
      const data = await api.read<Configuration>({
        kind: "configuration",
        project: project.id,
      });
      if (!alive.current) return;
      setSaved(data);
      setCompose(data.compose_yaml);
      setDotenv(data.env_file);
    } catch (e) {
      if (alive.current) setError(message(e));
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  async function deploy(e: FormEvent) {
    e.preventDefault();
    if (!saved) return;
    if (blueGreen) {
      setError(
        "Use Review blue–green below to check this draft before switching deployment strategy.",
      );
      return;
    }
    if (lintCompose(compose).some((p) => p.severity === "error")) {
      setFile("compose");
      setError("Fix the highlighted Compose syntax errors before deploying.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await execute({
        action: "deploy",
        project: project.id,
        data: {
          environment: project.environment,
          compose_yaml: compose,
          env_file: dotenv,
          expected_release_id: saved.release_id,
        },
      });
      if (alive.current) {
        setSaved(undefined);
        setCompose("");
        setDotenv("");
      }
    } catch (e) {
      if (alive.current) setError(message(e));
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  return (
    <section className="panel project-configuration">
      <div className="section-heading">
        <h2>
          Project files{" "}
          <Help label="Project files">
            Edit the saved release configuration. Files stay in this unlocked
            session until you deploy or leave this tab. Deployment validates
            both files and saves a new release. Referenced build contexts and
            additional files must exist on the VPS.
          </Help>
        </h2>
        {saved && (
          <button
            className="button small"
            disabled={busy || preview}
            onClick={load}
          >
            <RefreshCw size={16} /> Reload
          </button>
        )}
      </div>
      {error && (
        <p className="alert error" role="alert">
          {error}
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
              <p>Edit Docker Compose and .env, then deploy your changes.</p>
              <button
                className="button primary"
                disabled={preview}
                onClick={load}
              >
                <Fingerprint size={18} /> Edit files
              </button>
            </>
          )}
        </div>
      ) : (
        <form onSubmit={deploy}>
          {!project.zerodowntime && project.environment === "production" && (
            <fieldset className="deployment-strategy">
              <legend>Deployment strategy</legend>
              <div className="strategy-options">
                <button
                  type="button"
                  aria-pressed={!blueGreen}
                  className={!blueGreen ? "selected" : ""}
                  onClick={() => setBlueGreen(false)}
                >
                  <Box size={21} />
                  <span>
                    <strong>Single instance</strong>
                    <small>Replaces the current containers</small>
                  </span>
                </button>
                <button
                  type="button"
                  aria-pressed={blueGreen}
                  className={blueGreen ? "selected" : ""}
                  onClick={() => setBlueGreen(true)}
                >
                  <Layers3 size={21} />
                  <span>
                    <strong>Blue–green</strong>
                    <small>Health checks before switching traffic</small>
                  </span>
                </button>
              </div>
            </fieldset>
          )}
          <div className="configuration-toolbar">
            <div className="tabs" role="tablist" aria-label="Project files">
              <button
                type="button"
                role="tab"
                aria-selected={file === "compose"}
                className={file === "compose" ? "selected" : ""}
                onClick={() => setFile("compose")}
              >
                <FileCode2 size={18} aria-hidden="true" />
                compose.yaml
              </button>
              <button
                type="button"
                role="tab"
                aria-selected={file === "env"}
                className={file === "env" ? "selected" : ""}
                onClick={() => setFile("env")}
              >
                <Settings2 size={18} aria-hidden="true" />
                .env
              </button>
            </div>
            {file === "compose" && (
              <button
                type="button"
                className="button small"
                disabled={busy || preview}
                onClick={async () => {
                  try {
                    const value = await api.importCompose();
                    if (value && alive.current) setCompose(value);
                  } catch (e) {
                    setError(message(e));
                  }
                }}
              >
                <Upload size={15} /> Import Compose
              </button>
            )}
          </div>
          <div hidden={file !== "compose"}>
            <CodeEditor
              value={compose}
              onChange={setCompose}
              readOnly={busy || preview}
              active={file === "compose"}
            />
          </div>
          <div hidden={file !== "env"}>
            <CodeEditor
              kind="env"
              value={dotenv}
              onChange={setDotenv}
              readOnly={busy || preview}
              active={file === "env"}
            />
          </div>
          <div className="editor-meta">
            <span>
              {bytes(file === "compose" ? compose : dotenv).toLocaleString()} /
              65,536 bytes
            </span>
            <span>
              {preview
                ? "Sample files · read-only"
                : dirty
                  ? "Unsaved changes"
                  : "Saved configuration"}
            </span>
          </div>
          {file === "env" && (
            <p className="configuration-note">
              An empty .env clears saved values. Inline Compose environment
              values take precedence.
            </p>
          )}
          {blueGreen ? (
            <BlueGreenDeployment
              project={project}
              compose={compose}
              dotenv={dotenv}
              release={saved.release_id}
              preview={preview}
              execute={execute}
              onAccepted={() => {
                setSaved(undefined);
                setCompose("");
                setDotenv("");
                setBlueGreen(false);
              }}
            />
          ) : (
            <div className="configuration-footer">
              <span>
                {project.zerodowntime
                  ? "Health checks before traffic switches."
                  : "Single instance · deployment may briefly interrupt service."}
              </span>
              <button
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
                <ArrowUpRight size={18} />{" "}
                {busy ? "Preparing deployment…" : "Update & deploy"}
              </button>
            </div>
          )}
        </form>
      )}
    </section>
  );
}
