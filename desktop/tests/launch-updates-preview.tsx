// Development-only fixture. Every server action is simulated in memory.
import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import { Toaster } from "react-hot-toast";
import HomeServerUpdate from "../src/HomeServerUpdate";
import ServerUpdater from "../src/ServerUpdater";
import {
  ServerUpdatesProvider,
  useServerUpdateSession,
} from "../src/serverUpdates";
import type { ServerUpdatePreview } from "../src/api";
import "../src/style.css";
import "../src/macos.css";

let checks = 0;
let applies = 0;
let mode = "available";
let installed = "0.7.30";
let report: () => void = () => {};
mockIPC(async (command) => {
  if (command === "server_update_check") {
    checks++;
    report();
    await new Promise((resolve) => setTimeout(resolve, 150));
    if (mode === "offline")
      throw new Error("VPS unreachable. Retry when connected.");
    const value: ServerUpdatePreview = {
      source_commit: "a".repeat(40),
      candidate_version: "0.7.33",
      candidate_source: mode === "bundled" ? "bundled" : "github",
      release_check_error: mode === "bundled" ? "GitHub is unavailable" : null,
      installed_version: installed,
      installed_commit: "b".repeat(40),
      version_status: installed === "0.7.33" ? "current" : "update_available",
      update_available: installed !== "0.7.33",
      candidate_dockyard: "a".repeat(64),
      candidate_dockyardctl: "b".repeat(64),
      installed_dockyard: "c".repeat(64),
      installed_dockyardctl: "d".repeat(64),
      jobs: {
        sha256: "e".repeat(64),
        active_count: mode === "running" ? 1 : 0,
        recovery_count: 0,
        recovery_jobs: [],
        active_jobs:
          mode === "running"
            ? [
                {
                  job_id: "job-running",
                  project_id: "project",
                  action: "deploy",
                  status: "running",
                  phase: "starting",
                  error_code: "",
                },
              ]
            : [],
      },
    };
    return value;
  }
  if (command === "server_update_apply") {
    applies++;
    report();
    if (mode === "changed")
      throw new Error("Dockyard jobs changed. Check again before updating.");
    installed = "0.7.33";
    return installed;
  }
  if (command === "terminal_forget_password") return;
  throw new Error(`Unexpected fixture action: ${command}`);
});
const profile = {
  name: "Fixture",
  origin: "https://127.0.0.1",
  server_id: "fixture",
  key_id: "key",
  actor_id: "actor",
  server_certificate_sha256: "pin",
  server_ip: "192.0.2.1",
  ssh_port: 22,
  ssh_fingerprint: "ssh-pin",
};
function Fixture() {
  const [page, setPage] = useState("home");
  const [unlocked, setUnlocked] = useState(true);
  const [, rerender] = useState(0);
  const [selectedMode, setMode] = useState(mode);
  report = () => rerender((value) => value + 1);
  const updates = useServerUpdateSession(profile, unlocked);
  return (
    <ServerUpdatesProvider value={updates}>
      <main style={{ maxWidth: 1100, margin: "30px auto", padding: 24 }}>
        <p className="eyebrow">TEST FIXTURE · NO VPS ACCESS</p>
        <div className="controls">
          <button className="button" onClick={() => setPage("home")}>
            Home
          </button>
          <button className="button" onClick={() => setPage("details")}>
            Server details
          </button>
          <button
            className="button"
            onClick={() => setUnlocked((value) => !value)}
          >
            {unlocked ? "Lock" : "Unlock"}
          </button>
          <button
            className="button"
            onClick={() => rerender((value) => value + 1)}
          >
            Workspace refresh
          </button>
          <select
            aria-label="Server simulation"
            value={selectedMode}
            onChange={(event) => {
              mode = event.target.value;
              setMode(mode);
            }}
          >
            {["available", "running", "changed", "offline", "bundled"].map(
              (value) => (
                <option key={value}>{value}</option>
              ),
            )}
          </select>
        </div>
        <p role="status">
          Checks: {checks} · Updates: {applies} ·{" "}
          {unlocked ? "Unlocked" : "Locked"}
        </p>
        <h1>{page === "home" ? "Projects" : "Server details · Updates"}</h1>
        {unlocked ? (
          page === "home" ? (
            <HomeServerUpdate />
          ) : (
            <ServerUpdater />
          )
        ) : (
          <p>Unlock to continue. Cached versions are retained.</p>
        )}
        <Toaster />
      </main>
    </ServerUpdatesProvider>
  );
}
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Fixture />
  </StrictMode>,
);
