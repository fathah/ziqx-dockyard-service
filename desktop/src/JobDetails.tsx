import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Copy, RefreshCw, X } from "lucide-react";
import toast from "react-hot-toast";
import Button from "./Button";
import CopyButton from "./CopyButton";
import * as api from "./api";
import type { Job, Service } from "./types";

type Event = { status: string; phase: string; error_code?: string; time: string };

const phases: Record<string, string> = {
  accepted: "Queued",
  validating: "Validating",
  pulling: "Pulling images",
  candidate_intent: "Starting new version",
  readiness: "Health check",
  route_intent: "Switching traffic",
  maintenance_intent: "Maintenance",
  draining: "Draining old version",
  reconciled: "Recovered",
  dns_intent: "Updating DNS",
};
const sentence = (code: string) =>
  code.charAt(0) + code.slice(1).toLowerCase().replaceAll("_", " ");
const time = (s: string) =>
  new Date(s).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit", second: "2-digit" });
function duration(from: string, to: string): string {
  const s = Math.max(0, Math.round((new Date(to).getTime() - new Date(from).getTime()) / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}
// The logs API accepts fixed windows; pick the smallest covering the job.
function logWindow(created: string): string | undefined {
  const minutes = (Date.now() - new Date(created).getTime()) / 60000;
  if (minutes <= 5) return "5m";
  if (minutes <= 30) return "30m";
  if (minutes <= 60) return "1h";
  if (minutes <= 24 * 60) return "24h";
  return undefined;
}

export default function JobDetails({
  job,
  title,
  preview,
  onClose,
}: {
  job: Job;
  title: string;
  preview: boolean;
  onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [events, setEvents] = useState<Event[]>();
  const [diagnostic, setDiagnostic] = useState("");
  const [deployLog, setDeployLog] = useState<string>();
  const [services, setServices] = useState<string[]>([]);
  const [service, setService] = useState("");
  const [logs, setLogs] = useState<string>();
  const [error, setError] = useState("");
  const [logsError, setLogsError] = useState("");
  const [loadingLogs, setLoadingLogs] = useState(false);
  const since = logWindow(job.created_at);
  const active = job.status === "queued" || job.status === "running";

  useEffect(() => {
    const node = dialog.current;
    node?.showModal();
    return () => node?.close();
  }, []);

  async function loadEvents() {
    if (preview) {
      setEvents([
        { status: "queued", phase: "accepted", time: job.created_at },
        { status: job.status, phase: job.phase, error_code: job.error_code, time: job.finished_at ?? job.created_at },
      ]);
      return;
    }
    try {
      const data = await api.read<{ events: Event[]; diagnostic?: string }>({
        kind: "job_events",
        job: job.job_id,
      });
      setEvents(data.events);
      setDiagnostic(data.diagnostic ?? "");
      const log = await api
        .read<{ log: string }>({ kind: "job_log", job: job.job_id })
        .catch(() => ({ log: "" }));
      setDeployLog(log.log);
      setError("");
    } catch (e) {
      setError(/404|NOT_FOUND/.test(String(e)) ? "Update the VPS service to 0.7.9 to see the timeline." : String(e).replace(/^Error: /, ""));
    }
  }
  useEffect(() => {
    void loadEvents();
    if (preview) return;
    void api
      .read<{ services: Service[] }>({ kind: "project", project: job.project_id, view: "services" })
      .then((d) => {
        const names = Array.from(new Set((d.services ?? []).map((s) => s.name)));
        setServices(names);
        setService((current) => current || names[0] || "");
      })
      .catch(() => setServices([]));
  }, [job.job_id]);
  // Keep a running job's timeline current.
  useEffect(() => {
    if (!active || preview) return;
    const t = setInterval(() => void loadEvents(), 3000);
    return () => clearInterval(t);
  }, [active, job.job_id]);

  async function loadLogs() {
    if (!service || !since) return;
    setLoadingLogs(true);
    setLogsError("");
    try {
      const data = await api.read<{ logs: string; truncated: boolean }>({
        kind: "logs",
        project: job.project_id,
        service,
        slot: "active",
        tail: 300,
        since,
      });
      setLogs(data.logs || "No output in this window.");
    } catch (e) {
      setLogsError(String(e).replace(/^Error: /, ""));
    } finally {
      setLoadingLogs(false);
    }
  }
  useEffect(() => {
    if (service) void loadLogs();
  }, [service]);

  const failedAt = events?.some((e) => e.error_code);
  return createPortal(
    <dialog
      ref={dialog}
      className="job-details"
      aria-label={title}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      onClick={(e) => {
        if (e.target === dialog.current) onClose();
      }}
    >
      <header>
        <div>
          <h2>{title}</h2>
          <button
            type="button"
            className="job-details-id"
            title="Copy job ID"
            onClick={() => {
              void navigator.clipboard.writeText(job.job_id);
              toast.success("Job ID copied");
            }}
          >
            {job.job_id} <Copy size={12} />
          </button>
        </div>
        <Button type="button" className="icon-button" aria-label="Close" onClick={onClose}>
          <X size={18} />
        </Button>
      </header>

      <section>
        <h3>Timeline</h3>
        {error && <p className="job-details-note">{error}</p>}
        {!events && !error && <p className="job-details-note">Loading…</p>}
        {events && (
          <ol className="job-timeline">
            {events.map((e, i) => {
              const next = events[i + 1];
              const failed = !!e.error_code && e.status !== "succeeded";
              const done = e.status === "succeeded";
              const current = !next && (e.status === "running" || e.status === "queued");
              return (
                <li key={i} className={failed ? "failed" : done ? "done" : current ? "current" : ""}>
                  <i aria-hidden="true" />
                  <div>
                    <strong>
                      {done
                        ? "Completed"
                        : e.error_code === "JOB_INTERRUPTED_RECONCILED"
                          ? "Recovered after interruption"
                          : failed
                            ? sentence(e.error_code!)
                            : (phases[e.phase] ?? sentence(e.phase))}
                    </strong>
                    {failed && e.error_code !== "JOB_INTERRUPTED_RECONCILED" && (
                      <small>during {(phases[e.phase] ?? sentence(e.phase)).toLowerCase()}</small>
                    )}
                  </div>
                  <span>
                    {time(e.time)}
                    {next && <em>{duration(e.time, next.time)}</em>}
                  </span>
                </li>
              );
            })}
          </ol>
        )}
        {failedAt && job.status !== "succeeded" && !diagnostic && (
          <p className="job-details-note">Container logs below usually show why it failed.</p>
        )}
      </section>

      {deployLog ? (
        <section className="job-deploy-log">
          <div className="job-section-heading">
            <h3>Deployment log</h3>
            <CopyButton text={deployLog} />
          </div>
          <pre>
            {deployLog.split("\n").map((line, i) => (
              <span
                key={i}
                className={
                  line.startsWith("$ ")
                    ? "cmd"
                    : line.startsWith("== ")
                      ? "stage"
                      : line.startsWith("✗")
                        ? "bad"
                        : line.startsWith("✓")
                          ? "good"
                          : undefined
                }
              >
                {line.startsWith("== ") ? line.slice(3) : line}
                {"\n"}
              </span>
            ))}
          </pre>
        </section>
      ) : (
        diagnostic && (
          <section className="job-diagnostic">
            <div className="job-section-heading">
              <h3>Docker output</h3>
              <CopyButton text={diagnostic} />
            </div>
            <pre>{diagnostic}</pre>
          </section>
        )
      )}

      <section className="job-logs">
        <div className="job-logs-heading">
          <h3>Container logs</h3>
          {services.length > 0 && since && (
            <div>
              <select aria-label="Service" value={service} onChange={(e) => setService(e.target.value)}>
                {services.map((s) => (
                  <option key={s}>{s}</option>
                ))}
              </select>
              <Button
                type="button"
                className="icon-button"
                aria-label="Reload logs"
                disabled={loadingLogs}
                onClick={() => void loadLogs()}
              >
                <RefreshCw size={15} />
              </Button>
              <CopyButton text={logs && !/^No output/.test(logs) ? logs : ""} />
            </div>
          )}
        </div>
        {preview ? (
          <p className="job-details-note">Logs need a connected server.</p>
        ) : !since ? (
          <p className="job-details-note">Logs are kept for 24 hours. This operation is older.</p>
        ) : logsError ? (
          <p className="job-details-note">{logsError}</p>
        ) : (
          <pre>{loadingLogs && !logs ? "Loading…" : logs}</pre>
        )}
      </section>
    </dialog>,
    document.body,
  );
}
