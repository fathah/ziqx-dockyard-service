// Local-only fixture: simulate log failures, stalled IPC and late replies.
import { createRoot } from "react-dom/client";
import { useState } from "react";
import { mockIPC } from "@tauri-apps/api/mocks";
import ProjectLogs from "../src/ProjectLogs";
import type { Project, Service } from "../src/types";
import "../src/style.css";

const release = {
  id: "rel-logs",
  image: "example/api:latest",
  environment_revision: "env-logs",
  created_at: "2026-10-15T00:00:00Z",
};
const project: Project = {
  id: "logs-production",
  app_id: "logs",
  mode: "compose",
  environment: "production",
  state: "running",
  template_id: "",
  domains: [],
  route_service: "api",
  zerodowntime: true,
  blue_port: 3001,
  active_slot: "blue",
  slots: { blue: release, green: { ...release, id: "rel-standby" } },
  releases: [release],
};
const services: Service[] = [
  { name: "api", slot: "blue", ...release },
  { name: "worker", slot: "blue", ...release },
  { name: "previous-api", slot: "green", ...release },
].map((s) => ({ ...s, template_id: "" }));
let mode = "success";
let releaseStalled: (() => void) | undefined;
let rejectStalled: (() => void) | undefined;
let requests = 0;
mockIPC((command, payload: any) => {
  if (command !== "read_api" || payload.read.kind !== "logs")
    throw new Error("Unexpected fixture request");
  requests++;
  const logs = `${payload.read.service} | request ${requests} · ${payload.read.since}\n`;
  if (mode === "error") throw new Error("HTTP_503: LOGS_UNAVAILABLE");
  if (mode === "empty") return { logs: "", truncated: false };
  if (mode === "stalled")
    return new Promise((resolve, reject) => {
      releaseStalled = () => resolve({ logs: `LATE RESPONSE: ${logs}` });
      rejectStalled = () => reject("SESSION_LOCKED");
    });
  return { logs, truncated: mode === "truncated" };
});

function Fixture() {
  const [selected, setSelected] = useState(mode);
  const [reported, setReported] = useState("");
  const [enabled, setEnabled] = useState(true);
  return (
    <main style={{ maxWidth: 1100, padding: 32, margin: "auto" }}>
      <p>TEST FIXTURE · simulated container logs · no server access</p>
      <div style={{ display: "flex", gap: 10, marginBottom: 20 }}>
        {["success", "error", "empty", "stalled", "truncated"].map((value) => (
          <button
            key={value}
            onClick={() => {
              mode = value;
              setSelected(value);
            }}
          >
            Simulate {value}
          </button>
        ))}
        <button onClick={() => releaseStalled?.()}>Release late reply</button>
        <button onClick={() => rejectStalled?.()}>Reject late reply</button>
        <button onClick={() => setEnabled((value) => !value)}>
          {enabled ? "Lock fixture" : "Unlock fixture"}
        </button>
      </div>
      <p>Next request: {selected}</p>
      <ProjectLogs
        project={project}
        services={services}
        servicesLoading={false}
        preview={false}
        enabled={enabled}
        report={(error) => setReported(String(error))}
      />
      <p>Reported error: {reported || "none"}</p>
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
