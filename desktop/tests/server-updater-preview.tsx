// Development fixture. No SSH, uploads, restarts, or real Touch ID.
import { useState } from "react";
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import { Toaster } from "react-hot-toast";
import ServerUpdater from "../src/ServerUpdater";
import type { ServerUpdatePreview, UpdateJob } from "../src/api";
import "../src/style.css";

let mode = "recovery";
const job: UpdateJob = {
  job_id: "job-138604acae4a8d4aaedea60ee27681c7",
  project_id: "existing-6de23ea3626a4cbae142ed92",
  action: "start",
  status: "recovery_required",
  phase: "draining",
  error_code: "CONTAINER_STOP_FAILED",
};
function preview(): ServerUpdatePreview {
  const active = mode === "running";
  const recovery = mode === "recovery";
  return {
    source_commit: "local",
    candidate_version: "0.7.4",
    installed_version: "0.7.2",
    installed_commit: "local",
    version_status: "update_available",
    candidate_dockyard: "a".repeat(64),
    candidate_dockyardctl: "b".repeat(64),
    installed_dockyard: "c".repeat(64),
    installed_dockyardctl: "d".repeat(64),
    update_available: true,
    jobs: {
      sha256: "e".repeat(64),
      active_count: active ? 1 : 0,
      recovery_count: recovery ? 1 : 0,
      active_jobs: active
        ? [{ ...job, status: "running", error_code: "" }]
        : [],
      recovery_jobs: recovery ? [job] : [],
    },
  };
}
mockIPC((command) => {
  if (command === "server_update_check") {
    if (mode === "check-error")
      throw new Error(
        "Could not inspect Dockyard jobs. Check database access on the VPS before updating",
      );
    return preview();
  }
  if (command === "server_update_apply") {
    if (mode === "changed")
      throw new Error(
        "Dockyard jobs changed since your update review. Check the server again before updating",
      );
    if (mode === "running")
      throw new Error("The fixture must not apply while a job runs");
    return "0.7.4";
  }
  throw new Error("No server access in this fixture");
});
function Fixture() {
  const [revision, setRevision] = useState(0);
  const [message, setMessage] = useState(
    "All actions are simulated; no server access.",
  );
  return (
    <main style={{ maxWidth: 1050, margin: "35px auto", padding: 24 }}>
      <p className="eyebrow">TEST FIXTURE · NO SERVER ACCESS</p>
      <div className="controls">
        {["recovery", "running", "clear", "check-error"].map((value) => (
          <button
            className="button small"
            key={value}
            onClick={() => {
              mode = value;
              setRevision((n) => n + 1);
              setMessage(value + " selected");
            }}
          >
            {value}
          </button>
        ))}
        <button
          className="button small"
          onClick={() => {
            mode = "changed";
            setMessage("Server jobs changed after review");
          }}
        >
          Change jobs after review
        </button>
      </div>
      <p>{message}</p>
      <h1>Server details · Updates</h1>
      <ServerUpdater
        key={revision}
        openOperations={() => setMessage("View operations selected")}
      />
      <Toaster />
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
