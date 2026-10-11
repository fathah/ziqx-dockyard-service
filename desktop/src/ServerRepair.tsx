import { useState } from "react";
import { Fingerprint, Wrench, RefreshCw } from "lucide-react";
import Button from "./Button";
import * as api from "./api";

export const repairLabels: Record<string, string> = {
  caddy_admin: "Repair private connection",
  caddy_import: "Add route import",
  caddy_sites: "Prepare route folder",
  caddy_coherence: "Review route sync",
  caddy_validation: "Restore generated routes & validate",
  caddy_write: "Repair folder access",
  caddy_capability: "Repair socket access",
  caddy_service: "Start Caddy",
  docker: "Start Docker",
  docker_compose: "Install Compose plugin",
};

const connectionReady = (report: api.ServerAccessReport) =>
  report.checks
    .filter((check) => repairLabels[check.id] || check.id === "compose_access")
    .every((check) => check.status === "ready");

export default function ServerRepair({
  action,
  onRepaired,
  disabled,
}: {
  action: string;
  onRepaired?: (report: api.ServerAccessReport) => void;
  disabled?: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [review, setReview] = useState<api.RepairReview>();
  if (!repairLabels[action]) return null;
  return (
    <div className="server-repair">
      {review ? (
        <div className="repair-review">
          <strong>Review server repair</strong>
          <ul>
            {review.changes.map((change, i) => (
              <li key={i}>{change}</li>
            ))}
          </ul>
          {review.action.startsWith("caddy_") && (
            <>
              {review.preserved_domains?.length ? (
                <p>
                  Keep running sites: {review.preserved_domains.join(", ")}.
                  These routes will be saved before Caddy reloads.
                </p>
              ) : null}
              <details>
                <summary>Configuration comparison</summary>
                <p className="muted">
                  Saved sites:{" "}
                  {review.saved_available === false
                    ? "could not be read"
                    : review.saved_domains.join(", ") || "none"}
                  <br />
                  Live sites:{" "}
                  {review.live_available === false
                    ? "could not be read"
                    : review.live_domains.join(", ") || "none"}
                  <br />
                  Different settings:{" "}
                  {review.live_available === false ||
                  review.saved_available === false
                    ? "could not be compared"
                    : review.changed_sections?.join(", ") || "none"}
                  <br />
                  {review.reloads_caddy
                    ? "The saved configuration becomes active. "
                    : ""}
                  Dockyard keeps a private backup.
                </p>
              </details>
            </>
          )}
          <div className="heading-actions">
            <Button
              type="button"
              className="button primary small"
              disabled={busy || disabled}
              onClick={async () => {
                setBusy(true);
                setError("");
                try {
                  const report = await api.applyServerRepair(
                    review.action,
                    review.review_sha256,
                  );
                  setReview(undefined);
                  onRepaired?.(report);
                } catch (e) {
                  setError(String(e));
                  setReview(undefined);
                } finally {
                  setBusy(false);
                }
              }}
            >
              <Fingerprint size={15} />
              {busy ? "Repairing…" : "Apply repair"}
            </Button>
            <Button
              type="button"
              className="button small"
              disabled={busy}
              onClick={() => setReview(undefined)}
            >
              Cancel
            </Button>
          </div>
        </div>
      ) : (
        <Button
          type="button"
          className="button small"
          disabled={busy || disabled}
          onClick={async () => {
            setBusy(true);
            setError("");
            try {
              setReview(
                await api.previewServerRepair(
                  action === "caddy_validation" ? "caddy_coherence" : action,
                ),
              );
            } catch (e) {
              setError(String(e));
            } finally {
              setBusy(false);
            }
          }}
        >
          <Wrench size={15} />
          {busy ? "Reviewing…" : repairLabels[action]}
        </Button>
      )}
      {error && (
        <p className="route-error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

export function ConnectionHealth({
  onReady,
  disabled,
}: {
  onReady?: () => void;
  disabled?: boolean;
}) {
  const [report, setReport] = useState<api.ServerAccessReport>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const issues = report?.checks.filter(
    (check) =>
      check.status !== "ready" &&
      (repairLabels[check.id] ||
        check.id === "caddy_validation" ||
        check.id === "compose_access"),
  );
  return (
    <div className="connection-health">
      <Button
        type="button"
        className="button small"
        disabled={busy || disabled}
        onClick={async () => {
          setBusy(true);
          setError("");
          try {
            const next = await api.checkServerAccess();
            setReport(next);
            if (connectionReady(next)) onReady?.();
          } catch (e) {
            setError(String(e));
          } finally {
            setBusy(false);
          }
        }}
      >
        <RefreshCw size={15} />
        {busy ? "Checking server…" : "Check & repair connection"}
      </Button>
      {error && <p className="route-error">{error}</p>}
      {issues?.map((check) => (
        <div className="connection-health-issue" key={check.id}>
          <strong>{check.label}</strong>
          <p className="muted">{check.detail}</p>
          {check.id === "compose_access" ? (
            <Button
              type="button"
              className="button small"
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                setError("");
                try {
                  await api.enableComposeManagement();
                  const next = await api.checkServerAccess();
                  setReport(next);
                  if (connectionReady(next)) onReady?.();
                } catch (e) {
                  setError(String(e));
                } finally {
                  setBusy(false);
                }
              }}
            >
              Enable Compose management
            </Button>
          ) : (
            <ServerRepair
              action={check.id}
              onRepaired={(next) => {
                setReport(next);
                if (connectionReady(next)) onReady?.();
              }}
              disabled={disabled}
            />
          )}
        </div>
      ))}
      {report && !issues?.length && (
        <p className="muted" role="status">
          Connection checks passed. Review the domain again.
        </p>
      )}
    </div>
  );
}
