import { useState } from "react";
import { ArrowRight, Download, RefreshCw, X } from "lucide-react";
import Button from "./Button";
import ServerUpdater from "./ServerUpdater";
import { useServerUpdates } from "./serverUpdates";

export default function HomeServerUpdate({
  openOperations,
}: {
  openOperations?: () => void;
}) {
  const { preview, busy, error, attempted, checkedAt, check } =
    useServerUpdates();
  const [expanded, setExpanded] = useState(false);
  const [reviewOnOpen, setReviewOnOpen] = useState(false);
  const bundled = preview?.candidate_source === "bundled";
  const title = !preview
    ? busy
      ? "Checking server software…"
      : error
        ? "Couldn’t check server software"
        : "Server software"
    : preview.update_available
      ? bundled
        ? "Bundled Dockyard update available"
        : "Dockyard update available"
      : preview.version_status === "current"
        ? bundled
          ? "Bundled build matches your VPS"
          : "Dockyard is up to date"
        : preview.version_status === "server_newer"
          ? "Your VPS has a newer Dockyard version"
          : "Server software";
  const open = (review: boolean) => {
    setReviewOnOpen(review);
    setExpanded(true);
  };
  // The home screen only announces updates; versions live in server details.
  if (!expanded && !preview?.update_available) return null;
  return (
    <div className="home-server-update">
      {expanded ? (
        <>
          <div className="home-server-update-details-heading">
            <h2>VPS update</h2>
            <Button
              type="button"
              className="text-button"
              disabled={busy}
              onClick={() => setExpanded(false)}
            >
              <X size={14} /> Close details
            </Button>
          </div>
          <ServerUpdater
            initialReview={reviewOnOpen}
            openOperations={openOperations}
          />
        </>
      ) : (
        <section
          className={`panel home-server-update-card${preview?.update_available ? " has-update" : ""}`}
          aria-label="Server software"
        >
          <div className="home-server-update-summary">
            <div className="home-server-update-icon" aria-hidden="true">
              <Download size={20} />
            </div>
            <div className="home-server-update-copy">
              <h2>{title}</h2>
              {preview ? (
                <div className="home-server-update-versions">
                  <span>
                    VPS:{" "}
                    <strong>
                      {preview.installed_version
                        ? `v${preview.installed_version}`
                        : "Version unavailable"}
                    </strong>
                  </span>
                  <span>
                    {bundled ? "Bundled" : "Latest"}:{" "}
                    <strong>
                      {preview.candidate_version === "unversioned"
                        ? "Version unavailable"
                        : `v${preview.candidate_version}`}
                    </strong>
                  </span>
                  {checkedAt && (
                    <span className="home-server-update-checked">
                      Checked{" "}
                      {new Date(checkedAt).toLocaleTimeString([], {
                        hour: "numeric",
                        minute: "2-digit",
                      })}
                    </span>
                  )}
                </div>
              ) : (
                <p>
                  {error ||
                    (busy
                      ? "Reading the VPS version and latest GitHub release."
                      : attempted
                        ? "Check the connection, then try again."
                        : "Checks automatically when the app connects.")}
                </p>
              )}
              {preview && (error || preview.release_check_error) && (
                <p className="home-server-update-note" role="status">
                  {error
                    ? "The latest check failed. Showing the saved versions."
                    : "GitHub could not be checked. Showing this app’s bundled build."}
                </p>
              )}
            </div>
          </div>
          <div className="home-server-update-actions">
            {!preview ? (
              <Button
                type="button"
                className="button small"
                disabled={busy}
                onClick={() => void check()}
              >
                <RefreshCw size={14} />
                {busy ? "Checking…" : "Check version"}
              </Button>
            ) : (
              <>
                <Button
                  type="button"
                  className="text-button"
                  disabled={busy}
                  onClick={() => open(false)}
                >
                  Details
                </Button>
                {preview.update_available ? (
                  <Button
                    type="button"
                    className="button primary small"
                    disabled={busy}
                    onClick={() => open(true)}
                  >
                    Update server <ArrowRight size={14} />
                  </Button>
                ) : error || preview.release_check_error ? (
                  <Button
                    type="button"
                    className="button small"
                    disabled={busy}
                    onClick={() => void check()}
                  >
                    <RefreshCw size={14} />
                    {busy ? "Checking…" : "Check again"}
                  </Button>
                ) : null}
              </>
            )}
          </div>
        </section>
      )}
    </div>
  );
}
