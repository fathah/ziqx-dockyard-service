import Button from "./Button";
import { useEffect, useRef, useState, type FormEvent } from "react";
import {
  ArrowUpRight,
  FileCode2,
  Fingerprint,
  RefreshCw,
  Settings2,
  AlignLeft,
  Upload,
} from "lucide-react";
import * as api from "./api";
import Help from "./Help";
import CodeEditor, { type CodeEditorHandle } from "./CodeEditor";
import { composeForEditor, lintCompose } from "./composeLint";
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
  const initial: Configuration | undefined = preview
    ? {
        release_id: "preview",
        compose_yaml:
          "services:\n  web:\n    image: nginx:alpine\n    environment:\n      APP_URL: ${APP_URL}\n",
        env_file: "APP_URL=https://example.com\n",
      }
    : project.releases.length === 0
      ? { release_id: "", compose_yaml: "", env_file: "" }
      : undefined;
  const [saved, setSaved] = useState<Configuration | undefined>(initial);
  const [compose, setCompose] = useState(initial?.compose_yaml ?? "");
  const [dotenv, setDotenv] = useState(initial?.env_file ?? "");
  const [file, setFile] = useState<"compose" | "env">("compose");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
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
      const source = composeForEditor(data.compose_yaml);
      setSaved({ ...data, compose_yaml: source });
      setCompose(source);
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
      <div className="configuration-heading">
        <h2>
          Project files{" "}
          <Help label="Project files">
            Edit the saved release configuration. Files stay in this unlocked
            session until you deploy or leave this tab. Deployment validates
            both files and saves a new release. Referenced build contexts and
            additional files must exist on the VPS.
          </Help>
        </h2>
      </div>
      <p className="configuration-subtitle">
        {project.environment} · /docker/
        {project.adoption?.source_name ?? project.id}/compose.yml and .env
        {project.releases.length === 0 &&
          " · Import or paste both files to deploy your services."}
      </p>
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
              <Button
                type="button"
                className="button primary"
                disabled={preview}
                onClick={load}
              >
                <Fingerprint size={18} /> Edit files
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
                      else setDotenv(value);
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
          <div hidden={file !== "env"}>
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
                  : saved.release_id
                    ? "Saved configuration"
                    : "New deployment"}
            </span>
          </div>
          {file === "env" && (
            <p className="configuration-note">
              An empty .env clears saved values. Inline Compose environment
              values take precedence.
            </p>
          )}
          <div className="configuration-footer">
            <span>
              {project.zerodowntime
                ? "Health checks before traffic switches."
                : "Deploys all services with a maintenance interruption. Use Pull & update in Services for individual updates."}
            </span>
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
              <ArrowUpRight size={18} />{" "}
              {busy
                ? "Preparing deployment…"
                : saved.release_id
                  ? "Deploy all services"
                  : "Deploy all services"}
            </Button>
          </div>
        </form>
      )}
    </section>
  );
}
