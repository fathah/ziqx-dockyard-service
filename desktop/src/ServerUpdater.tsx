import { useEffect, useRef, useState } from "react";
import { ArrowRight, RefreshCw } from "lucide-react";
import toast from "react-hot-toast";
import Button from "./Button";
import * as api from "./api";
import { useServerUpdates } from "./serverUpdates";

const explain = (error: unknown) =>
  error instanceof Error ? error.message : String(error);

export default function ServerUpdater({
  openOperations,
  initialReview = false,
}: {
  openOperations?: () => void;
  initialReview?: boolean;
}) {
  const {
    preview,
    busy: updateBusy,
    error: updateError,
    check,
    apply,
  } = useServerUpdates();
  const [passwordBusy, setPasswordBusy] = useState(false);
  const [passwordError, setPasswordError] = useState("");
  const [review, setReview] = useState<api.ServerUpdatePreview | null>(null);
  const initialReviewStarted = useRef(false);
  const busy = updateBusy || passwordBusy;
  const error = passwordError || updateError;
  const jobs = preview?.jobs;
  const blocked = !review || review.jobs.active_count > 0;
  async function checkVersion() {
    setPasswordError("");
    setReview(null);
    await check();
  }
  async function beginReview() {
    setPasswordError("");
    setReview(null);
    const fresh = await check();
    if (fresh?.update_available && fresh.jobs.active_count === 0)
      setReview(fresh);
  }
  useEffect(() => {
    if (initialReview && !initialReviewStarted.current) {
      initialReviewStarted.current = true;
      void beginReview();
    }
  }, [initialReview]);
  async function update() {
    if (!review || blocked) return;
    const expected = review;
    setPasswordError("");
    const updated = await apply(expected);
    setReview(null);
    if (updated) {
      toast.success(
        expected.candidate_version === "unversioned"
          ? `Dockyard service updated to bundled build ${expected.source_commit.slice(0, 12)}.`
          : `Dockyard service updated to v${expected.candidate_version}.`,
      );
      if (expected.jobs.recovery_count > 0)
        toast(
          "Control service updated. The recovery job is preserved and still blocks deployments.",
        );
    }
  }
  async function forgetPassword() {
    setPasswordBusy(true);
    setPasswordError("");
    try {
      await api.forgetRootPassword();
      toast.success("Saved root password removed from Keychain.");
    } catch (e) {
      setPasswordError(explain(e));
    } finally {
      setPasswordBusy(false);
    }
  }
  return (
    <section className="panel server-updater">
      <div className="server-updater-heading">
        <div>
          <h2>Server software</h2>
          <p className="muted">
            Checks once when the app connects. Check again to refresh the VPS
            version and update safety checks.
          </p>
        </div>
        <Button
          type="button"
          className="button"
          disabled={busy}
          onClick={() => void checkVersion()}
        >
          <RefreshCw size={16} />
          {busy ? "Working…" : "Check version"}
        </Button>
      </div>
      {preview && (
        <div className="server-updater-status">
          <span className="tag">
            {preview.version_status === "current"
              ? preview.candidate_source === "bundled"
                ? "Bundled build matches VPS"
                : "Up to date"
              : preview.version_status === "server_newer"
                ? "VPS is newer"
                : preview.version_status === "same_version_different_build"
                  ? "Same version, different build"
                  : preview.version_status === "bundle_unversioned"
                    ? "Bundle needs rebuild"
                    : preview.version_status === "legacy_bundle"
                      ? "Unversioned update"
                      : "Update available"}
          </span>
          <span>
            VPS:{" "}
            {preview.installed_version
              ? `v${preview.installed_version}`
              : "Version unavailable"}
          </span>
          <span>
            {preview.candidate_source === "bundled" ? "Bundled" : "Latest"}:{" "}
            {preview.candidate_version === "unversioned"
              ? "Version unavailable"
              : `v${preview.candidate_version}`}
          </span>
          {preview.update_available && !review && (
            <Button
              type="button"
              className="button primary"
              disabled={busy}
              onClick={() => void beginReview()}
            >
              Update server <ArrowRight size={15} />
            </Button>
          )}
        </div>
      )}
      {preview?.release_check_error && (
        <div className="server-update-release-error" role="status">
          <p>
            GitHub could not be checked. Showing the verified build bundled with
            this app. Use Check version to retry.
          </p>
          <span>{preview.release_check_error}</span>
        </div>
      )}
      {jobs && (jobs.active_count > 0 || jobs.recovery_count > 0) && (
        <div className="server-update-jobs" role="status">
          <h3>
            {jobs.active_count > 0
              ? "Update blocked by active work"
              : preview?.update_available
                ? "Recovery job will be preserved"
                : "Recovery still required"}
          </h3>
          <p>
            {jobs.active_count > 0
              ? `${jobs.active_count} queued or running operation${jobs.active_count === 1 ? "" : "s"}. Wait for completion, then check again before updating.`
              : `${jobs.recovery_count} operation${jobs.recovery_count === 1 ? " requires" : "s require"} inspection. Updating the control service is allowed, but will not clear the job or unlock deployments.`}
          </p>
          {[...jobs.active_jobs, ...jobs.recovery_jobs].map((job) => (
            <div className="server-update-job" key={job.job_id}>
              <strong>
                {job.action.replaceAll("_", " ")} · {job.project_id}
              </strong>
              <span>
                {job.status.replaceAll("_", " ")} · {job.phase}
                {job.error_code && ` · ${job.error_code}`}
              </span>
              <code>{job.job_id}</code>
            </div>
          ))}
          {openOperations && (
            <Button
              type="button"
              className="button small"
              disabled={busy}
              onClick={openOperations}
            >
              View operations
            </Button>
          )}
        </div>
      )}
      {preview?.version_status === "bundle_unversioned" && (
        <p className="muted">
          This Mac app contains an older, unversioned Ubuntu binary. Bundle the
          current Go build to enable in-app updates.
        </p>
      )}
      {review && (
        <div className="server-updater-review">
          <h3>Update Dockyard on your VPS?</h3>
          <p>
            {review.candidate_version === "unversioned"
              ? "This older bundle has no version number. Check its source commit before installing; the control service will restart briefly."
              : "The control service will restart briefly. Containers and Caddy will continue; Dockyard keeps a database and binary backup for rollback."}
          </p>
          {review.jobs.recovery_count > 0 && (
            <p>
              <strong>The recovery lock stays in place.</strong> This update
              replaces Dockyard’s binaries and restarts its control service. It
              does not restart containers, import VPS Compose edits, or repair
              the failed operation.
            </p>
          )}
          <div className="server-updater-hashes">
            <span>Installed version</span>
            <strong>
              {review.installed_version
                ? `v${review.installed_version}`
                : "Unavailable (older build)"}
            </strong>
            <span>New build</span>
            <strong>
              {review.candidate_version === "unversioned"
                ? `Unversioned · ${review.source_commit.slice(0, 12)}`
                : `v${review.candidate_version}`}
            </strong>
          </div>
          <div className="controls">
            <Button
              type="button"
              className="button primary"
              disabled={busy || blocked}
              onClick={() => void update()}
            >
              {busy ? "Updating…" : "Confirm update with Touch ID"}
            </Button>
            <Button
              type="button"
              className="button"
              disabled={busy}
              onClick={() => setReview(null)}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}
      <Button
        type="button"
        className="text-button server-updater-forget"
        disabled={busy}
        onClick={() => void forgetPassword()}
      >
        Forget saved root password
      </Button>
      {error && (
        <div className="alert error" role="alert">
          {error}
        </div>
      )}
    </section>
  );
}
