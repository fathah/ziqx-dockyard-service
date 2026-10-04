// Development fixture. No native operations or server connections.
import React, { useState } from "react";
import { createRoot } from "react-dom/client";
import { RefreshCw } from "lucide-react";
import { Toaster } from "react-hot-toast";
import Button from "../src/Button";
import ServerDetailsTabs from "../src/ServerDetailsTabs";
import ServerDiagnostics from "../src/ServerDiagnostics";
import type { ServerAccessReport } from "../src/api";
import "../src/style.css";

const checks: ServerAccessReport["checks"] = [
  {
    id: "docker",
    label: "Docker daemon",
    status: "ready",
    detail: "Docker responds to the configured client.",
  },
  {
    id: "docker_compose",
    label: "Docker Compose",
    status: "ready",
    detail: "The Compose plugin is available.",
  },
  {
    id: "caddy_service",
    label: "Caddy service",
    status: "ready",
    detail: "Caddy is running.",
  },
  {
    id: "caddy_import",
    label: "Dockyard route import",
    status: "failed",
    detail:
      "Seamless traffic switching needs a top-level import for the configured generated route directory. Review existing files before adding it.",
    repair:
      'sudo ls -la /etc/caddy/dockyard\nsudo nano /etc/caddy/Caddyfile\n# Add this line at the top level of the Caddyfile:\n# import "/etc/caddy/dockyard/*.caddy"',
  },
  {
    id: "caddy_sites",
    label: "Generated route files",
    status: "ready",
    detail:
      "The route directory is empty; Dockyard creates files during deployment.",
  },
  {
    id: "caddy_validation",
    label: "Caddy configuration",
    status: "ready",
    detail: "The saved Caddy configuration is valid.",
  },
  {
    id: "caddy_admin",
    label: "Private Caddy connection",
    status: "ready",
    detail: "The private admin socket responds with live configuration.",
  },
  {
    id: "caddy_coherence",
    label: "Saved and live routes",
    status: "ready",
    detail: "Saved routes match the running Caddy configuration.",
  },
  {
    id: "caddy_write",
    label: "Caddy folder write access",
    status: "ready",
    detail:
      "Both Caddy folders are writable inside Dockyard’s service mount namespace.",
  },
  {
    id: "caddy_capability",
    label: "Private socket permissions",
    status: "ready",
    detail: "Dockyard has CAP_DAC_OVERRIDE to reach Caddy’s private socket.",
  },
  {
    id: "job_recovery",
    label: "Job recovery",
    status: "ready",
    detail: "No jobs are waiting for recovery.",
  },
  {
    id: "database_read",
    label: "Job history access",
    status: "ready",
    detail:
      "Recent failed jobs were read using Python’s built-in SQLite support.",
  },
  {
    id: "recent_logs",
    label: "Recent server logs",
    status: "warning",
    detail:
      "Last 300 journal entries within two hours: 1 upstream connection failures. These may concern other projects.",
    repair:
      'sudo journalctl -u dockyard -u caddy --since "2 hours ago" -n 100 --no-pager -o short-iso',
  },
];
const initial: ServerAccessReport = {
  root_ssh: true,
  update_directory: "ready",
  installed_binaries: true,
  database: true,
  service_active: true,
  checks,
  recent_jobs: [
    {
      job_id: "job-example",
      project_id: "demo-production",
      action: "service_update",
      status: "failed",
      phase: "service_route_intent",
      error_code: "BLUE_GREEN_ROUTE_REVIEW_REQUIRED",
      finished_at: "2026-10-04T10:53:00Z",
    },
  ],
};

function Fixture() {
  const [report, setReport] = useState(initial);
  return (
    <main style={{ maxWidth: 1080, margin: "40px auto", padding: 24 }}>
      <p className="eyebrow">TEST FIXTURE · NO SERVER ACCESS</p>
      <div className="page-heading">
        <h1>Server details</h1>
        <span className="tag green">Unlocked</span>
      </div>
      <ServerDetailsTabs
        connection={
          <section className="panel enrollment-panel">
            <h2>Production VPS</h2>
            <dl className="facts">
              <dt>API origin</dt>
              <dd className="mono">https://127.0.0.1:9123</dd>
              <dt>Server ID</dt>
              <dd>vps-01</dd>
              <dt>Credential ID</dt>
              <dd>desktop-01</dd>
            </dl>
          </section>
        }
        security={
          <section className="panel security-list">
            <h2>Layered access controls</h2>
            <p>Client certificate · Signed requests · Touch ID & Keychain</p>
          </section>
        }
        updates={
          <section className="panel server-updater">
            <h2>Server updates</h2>
            <p className="muted">Your server is up to date.</p>
          </section>
        }
        access={
          <section className="panel server-access">
            <div className="server-updater-heading">
              <div>
                <h2>Required server access</h2>
                <p className="muted">
                  Check permissions, routes, and deployment readiness before
                  updating services.
                </p>
              </div>
              <Button
                className="button"
                onClick={() =>
                  setReport({
                    ...report,
                    checks: report.checks.map((check) => ({
                      ...check,
                      status: "ready",
                      detail:
                        check.id === "caddy_import"
                          ? "The Caddyfile loads the generated route directory."
                          : check.id === "recent_logs"
                            ? "No recognized errors in the recent journal."
                            : check.detail,
                      repair: null,
                    })),
                  })
                }
              >
                <RefreshCw size={16} />
                Check access
              </Button>
            </div>
            <p className="muted server-access-checked">
              Checked just now · Run again after changing server settings.
            </p>
            <div className="server-access-row">
              <span>Compose management</span>
              <span className="tag green">Enabled</span>
            </div>
            <ServerDiagnostics report={report} />
          </section>
        }
      />
      <Toaster />
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
