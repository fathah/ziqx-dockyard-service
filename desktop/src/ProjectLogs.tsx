import { useEffect, useRef, useState } from "react";
import { RefreshCw, Terminal } from "lucide-react";
import * as api from "./api";
import Button from "./Button";
import CopyButton from "./CopyButton";
import Help from "./Help";
import Select from "./Select";
import type { Project, Service } from "./types";

const instanceLabel = (slot: string) =>
  ({
    blue: "Instance 1",
    green: "Instance 2",
    active: "Live instance",
    inactive: "Standby instance",
  })[slot] ?? slot;

type LogResult = {
  key: string;
  status: "loading" | "ready" | "error";
  logs?: string;
  truncated?: boolean;
  error?: string;
};

export default function ProjectLogs({
  project,
  services,
  servicesLoading,
  servicesError = false,
  preview,
  enabled = true,
  report,
}: {
  project: Project;
  services: Service[];
  servicesLoading: boolean;
  servicesError?: boolean;
  preview: boolean;
  enabled?: boolean;
  report: (error: unknown) => void;
}) {
  const [service, setService] = useState(project.route_service ?? "");
  const [slot, setSlot] = useState("active");
  const [since, setSince] = useState("30m");
  const [result, setResult] = useState<LogResult>();
  const cancelRequest = useRef<(() => void) | undefined>(undefined);
  const resolvedSlot =
    slot === "active"
      ? (project.active_slot ?? "blue")
      : slot === "inactive"
        ? project.active_slot === "blue"
          ? "green"
          : "blue"
        : slot;
  const names = Array.from(
    new Set(services.filter((s) => s.slot === resolvedSlot).map((s) => s.name)),
  );
  if (!names.length && project.mode !== "compose") names.push("app");
  const selectedService = names.includes(service) ? service : (names[0] ?? "");
  const key = JSON.stringify([
    project.id,
    selectedService,
    resolvedSlot,
    project.slots[resolvedSlot]?.id,
    project.service_instances?.[selectedService]?.name,
    since,
    preview,
    enabled,
  ]);
  const current = result?.key === key ? result : undefined;
  const loading = current?.status === "loading";
  const logs = current?.status === "ready" ? (current.logs ?? "") : "";

  // Changing filters, leaving Logs, or locking the workspace invalidates the
  // response. A late IPC reply must never replace a newer selection or retry.
  useEffect(() => {
    setResult(undefined);
    return () => {
      cancelRequest.current?.();
      cancelRequest.current = undefined;
    };
  }, [key]);

  function loadLogs() {
    if (!enabled || !selectedService || servicesLoading || loading) return;
    cancelRequest.current?.();
    let active = true;
    const cancel = () => {
      active = false;
      clearTimeout(timer);
    };
    const finish = (next: LogResult) => {
      if (!active) return;
      cancel();
      cancelRequest.current = undefined;
      setResult(next);
    };
    // Include native IPC/session-lock waiting, which the HTTP timeout excludes.
    const timer = setTimeout(
      () =>
        finish({
          key,
          status: "error",
          error: "The log request timed out. Read logs to try again.",
        }),
      35_000,
    );
    cancelRequest.current = cancel;
    setResult({ key, status: "loading" });
    if (preview) {
      finish({
        key,
        status: "ready",
        logs: `[Sample logs · no server connected]\n${selectedService}  | Listening on 0.0.0.0:3000\n${selectedService}  | Readiness check passed\n`,
      });
      return;
    }
    void api
      .read<{ logs: string; truncated: boolean }>({
        kind: "logs",
        project: project.id,
        service: selectedService,
        slot,
        tail: 200,
        since,
      })
      .then((data) => {
        if (typeof data.logs !== "string")
          throw new Error("The server returned an invalid log response.");
        finish({
          key,
          status: "ready",
          logs: data.logs,
          truncated: data.truncated,
        });
      })
      .catch((error) => {
        if (!active) return;
        finish({
          key,
          status: "error",
          error: `Could not read container logs. ${String(error)}`,
        });
        report(error);
      });
  }

  return (
    <section className="panel logs-panel">
      <div className="section-heading">
        <h2>
          <Terminal size={18} /> Container logs
        </h2>
        <div className="heading-actions">
          <span className="tag neutral">Bounded · 200 lines</span>
          <CopyButton text={logs} />
        </div>
      </div>
      <div className="log-filters">
        <Select
          label="Service"
          value={selectedService}
          onValueChange={setService}
          placeholder={servicesLoading ? "Loading services…" : "No services"}
          options={names.map((name) => ({ value: name, label: name }))}
        />
        <Select
          label="Instance"
          value={slot}
          onValueChange={setSlot}
          options={[
            "active",
            ...(project.zerodowntime
              ? ["inactive", "blue", "green"]
              : ["blue"]),
          ].map((value) => ({ value, label: instanceLabel(value) }))}
        />
        <Select
          label="Since"
          value={since}
          onValueChange={setSince}
          options={["5m", "30m", "1h", "24h"].map((value) => ({
            value,
            label: value,
          }))}
        />
        <Button
          type="button"
          className="button"
          disabled={!enabled || loading || servicesLoading || !selectedService}
          onClick={loadLogs}
        >
          <RefreshCw size={14} /> {loading ? "Reading logs…" : "Read logs"}
        </Button>
      </div>
      {current?.status === "error" && (
        <p className="alert error" role="alert">
          {current.error}
        </p>
      )}
      <pre
        className="terminal"
        aria-busy={loading}
        role={loading ? "status" : undefined}
      >
        {loading
          ? "Reading container logs…"
          : logs ||
            (current?.status === "ready"
              ? `No logs for this service in the last ${since}. Try a longer time range.`
              : current?.status === "error"
                ? "Read logs to try again."
                : servicesLoading
                  ? "Loading available services…"
                  : !selectedService
                    ? servicesError
                      ? "Could not load services. Refresh the Services tab and try again."
                      : "No services recorded for this instance."
                    : "Select a service and read its logs.")}
      </pre>
      {current?.status === "ready" && current.truncated && (
        <p className="warning">Output was truncated by the server.</p>
      )}
      <div className="help-row">
        Log privacy{" "}
        <Help label="log privacy">
          The server redacts retained environment values. Avoid logging secrets
          in your applications.
        </Help>
      </div>
    </section>
  );
}
