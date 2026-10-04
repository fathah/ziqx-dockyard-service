import { CheckCircle2, CircleAlert, Copy, History } from "lucide-react";
import toast from "react-hot-toast";
import Button from "./Button";
import type { ServerAccessReport } from "./api";

export default function ServerDiagnostics({
  report,
}: {
  report: ServerAccessReport;
}) {
  if (!report.checks?.length) {
    return (
      <p className="muted">
        Rebuild and reopen the Mac app to use the new deployment health checks.
      </p>
    );
  }
  const checks = report.checks
    .filter((check) => check.id !== "compose_access")
    .sort((a, b) => {
      const order = { failed: 0, warning: 1, ready: 2 };
      return order[a.status] - order[b.status];
    });
  const flagged = report.checks.filter((check) => check.status !== "ready").length;
  return (
    <div className="server-diagnostics">
      <div className="section-heading">
        <h3>Deployment readiness</h3>
        <span className={`tag ${flagged ? "amber" : "green"}`} role="status">
          {flagged ? `${flagged} to review` : "All checks passed"}
        </span>
      </div>
      <p className="muted">
        Checks use the server’s configured paths and leave saved routes and
        services unchanged.
      </p>
      <div className="server-diagnostic-list">
        {checks.map((check) => (
          <div className={`server-diagnostic ${check.status}`} key={check.id}>
            {check.status === "ready" ? (
              <CheckCircle2 size={19} aria-hidden="true" />
            ) : (
              <CircleAlert size={19} aria-hidden="true" />
            )}
            <div className="server-diagnostic-content">
              <div className="server-diagnostic-heading">
                <strong>{check.label}</strong>
                <span
                  className={`tag ${check.status === "ready" ? "green" : check.status === "warning" ? "amber" : "red"}`}
                >
                  {check.status === "ready"
                    ? "Ready"
                    : check.status === "warning"
                      ? "Warning"
                      : "Needs attention"}
                </span>
              </div>
              <p>{check.detail}</p>
              {check.repair && (
                <details className="connection-details">
                  <summary>Review on the server</summary>
                  <pre>{check.repair}</pre>
                  <Button
                    type="button"
                    className="button small"
                    onClick={async () => {
                      try {
                        await navigator.clipboard.writeText(check.repair!);
                        toast.success("Review commands copied.");
                      } catch {
                        toast.error(
                          "Could not copy. Select the commands above.",
                        );
                      }
                    }}
                  >
                    <Copy size={14} /> Copy commands
                  </Button>
                </details>
              )}
            </div>
          </div>
        ))}
      </div>
      <div className="section-heading server-diagnostic-history-heading">
        <h3>
          <History size={17} aria-hidden="true" /> Recent failed operations
        </h3>
        <span className="muted">Latest five</span>
      </div>
      {report.recent_jobs.length === 0 ? (
        <p className="muted">
          {checks.find((check) => check.id === "database_read")?.status ===
          "ready"
            ? "No failed operations recorded."
            : "Job history is unavailable until the database check passes."}
        </p>
      ) : (
        report.recent_jobs.map((job) => (
          <div className="server-diagnostic-job" key={job.job_id}>
            <div className="server-diagnostic-heading">
              <strong>
                {job.action.replaceAll("_", " ")} · {job.project_id}
              </strong>
              <span
                className={`tag ${job.status === "recovery_required" ? "amber" : "red"}`}
              >
                {job.status === "recovery_required"
                  ? "Recovery required"
                  : "Failed"}
              </span>
            </div>
            <p className="mono">
              {job.phase} · {job.error_code}
            </p>
            {job.error_code === "BLUE_GREEN_ROUTE_REVIEW_REQUIRED" && (
              <p>
                Check the route import and generated files above. If those pass,
                the site may use routing that needs manual review.
              </p>
            )}
            <small className="muted mono">{job.job_id}</small>
            {Number.isFinite(Date.parse(job.finished_at)) && (
              <time dateTime={job.finished_at}>
                {new Date(job.finished_at).toLocaleString()}
              </time>
            )}
          </div>
        ))
      )}
    </div>
  );
}
