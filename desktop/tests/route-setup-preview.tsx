// Development-only fixture: all reads and route writes are simulated locally.
import { useState } from "react";
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import { ProjectDetail } from "../src/App";
import type { Project, Service, Job } from "../src/types";
import "../src/style.css";

const release = {
  id: "rel-original",
  image: "",
  environment_revision: "env-original",
  created_at: "2026-10-04T00:00:00Z",
};
const original: Project = {
  id: "tasks-production",
  app_id: "ziqx-tasks",
  environment: "production",
  mode: "compose",
  template_id: "",
  domains: [],
  external_domains: ["tasks.example.com"],
  adoption: { source_name: "ziqx-tasks", compose_project: "ziqx-tasks" },
  zerodowntime: false,
  blue_port: 0,
  active_slot: "blue",
  state: "running",
  slots: { blue: release },
  releases: [release],
};
const services: Service[] = [
  {
    name: "app",
    slot: "blue",
    image: "tasks:latest",
    depends_on: ["postgres"],
    updatable: true,
    seamless: false,
    container_ports: [3000],
    environment_revision: "env-original",
    template_id: "",
  },
  {
    name: "postgres",
    slot: "blue",
    image: "postgres:17",
    depends_on: [],
    updatable: true,
    seamless: false,
    container_ports: [5432],
    environment_revision: "env-original",
    template_id: "",
  },
];
const interrupted: Job = {
  job_id: "job-138604acae4a8d4aaedea60ee27681c7",
  project_id: original.id,
  action: "start",
  status: "recovery_required",
  phase: "draining",
  error_code: "CONTAINER_STOP_FAILED",
  actor_id: "fixture",
  request_id: "fixture",
  created_at: "2026-10-04T11:31:00Z",
};
let mode = "ready",
  applied = false;
mockIPC((command, payload: any) => {
  if (command === "read_api") {
    switch (payload.read?.view) {
      case "services":
        return { services };
      case "status":
        return {
          health: "healthy",
          route: "ok",
          busy: mode === "recovery",
          public_tls_state: "unverified",
        };
      case "domains":
        return applied
          ? [{ hostname: "tasks.example.com", assigned: true }]
          : [];
    }
  }
  if (command === "preview_route_setup") {
    if (payload.data.service !== "app" || payload.data.container_port !== 3000)
      throw new Error("BLUE_GREEN_ROUTE_TARGET_MISMATCH");
    if (mode === "changed") throw new Error("ROUTE_REVIEW_CHANGED");
    return {
      review_sha256: "a".repeat(64),
      domains: payload.data.domains,
      service: "app",
      container_port: 3000,
      upstream: "127.0.0.1:3031",
      imports_routes: true,
      containers_unchanged: true,
    };
  }
  throw new Error("No server access in this fixture");
});
function Fixture() {
  const [project, setProject] = useState(original);
  const [message, setMessage] = useState(
    "All actions are simulated; no server access.",
  );
  const [revision, setRevision] = useState(0);
  return (
    <main style={{ padding: 32, maxWidth: 1150, margin: "auto" }}>
      <p className="eyebrow">TEST FIXTURE · NO SERVER ACCESS</p>
      <div className="controls">
        {["ready", "recovery", "stopped", "changed"].map((value) => (
          <button
            key={value}
            className="button small"
            onClick={() => {
              mode = value;
              applied = false;
              setProject({
                ...original,
                state:
                  value === "recovery" || value === "stopped"
                    ? "stopped"
                    : "running",
              });
              setRevision((n) => n + 1);
              setMessage(value + " selected");
            }}
          >
            {value}
          </button>
        ))}
      </div>
      <p>{message}</p>
      <ProjectDetail
        key={revision}
        project={project}
        initialTab="domains"
        preview={false}
        jobs={mode === "recovery" ? [interrupted] : []}
        report={(error) => setMessage(String(error))}
        open={() => setMessage("Edit route shortcut works")}
        action={async () => {
          throw new Error("No container writes allowed");
        }}
        openServerDetails={() => setMessage("Server checks shortcut works")}
        openDeployments={() => setMessage("Operations shortcut works")}
        execute={async (mutation: any) => {
          if (
            mutation.action !== "route_setup" ||
            mutation.data.review_sha256 !== "a".repeat(64)
          )
            throw new Error("Unreviewed mutation rejected");
          applied = true;
          setProject({
            ...original,
            domains: original.external_domains!,
            external_domains: [],
            blue_port: 3031,
            published_route: {
              service: "app",
              container_port: 3000,
              host_port: 3031,
            },
          });
          setMessage("Simulated route configured; containers unchanged.");
        }}
      />
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
