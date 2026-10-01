// Development-only fixture: all reads are simulated; mutations are rejected.
import React, { useState } from "react";
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import { Jobs, ProjectDetail } from "../src/App";
import type { Job, Project, Service } from "../src/types";
import "../src/style.css";

let mode = "legacy";
const interrupted: Job = {
  job_id: "job-bd0bd219b387d591fa22751a1c2262b6",
  project_id: "shop-production",
  action: "service_update",
  status: "recovery_required",
  phase: "pulling",
  error_code: "JOB_INTERRUPTED",
  actor_id: "fixture",
  request_id: "req-fixture",
  created_at: "2026-10-01T11:35:06Z",
};
const release = {
  id: "rel-original",
  image: "",
  environment_revision: "env-original",
  created_at: "2026-10-01T00:00:00Z",
};
const original: Project = {
  id: "shop-production",
  app_id: "shop",
  environment: "production",
  mode: "compose",
  template_id: "",
  domains: ["shop.example.com"],
  zerodowntime: false,
  blue_port: 4100,
  active_slot: "blue",
  state: "running",
  slots: { blue: release },
  releases: [release],
  route_service: "web",
  route_port: 3000,
};
let serviceReads = 0;
let replaced = false;
mockIPC((command, payload: any) => {
  if (command !== "read_api" || payload.read.project !== original.id)
    throw new Error("Unexpected operation: " + command);
  switch (payload.read.view) {
    case "services": {
      serviceReads++;
      if (mode === "error") throw new Error("Simulated read failure");
      const items: Service[] = [
        {
          name: "web",
          slot: "blue",
          image: replaced ? "example/shop:v2" : "example/shop:latest",
          template_id: "",
          environment_revision: "env-original",
        },
        {
          name: "postgres",
          slot: "blue",
          image: "postgres:17",
          template_id: "",
          environment_revision: "env-original",
        },
      ];
      if (mode !== "legacy") {
        items[0] = {
          ...items[0],
          depends_on: ["postgres"],
          updatable: mode !== "unavailable",
          seamless: mode !== "unavailable",
          container_ports: [3000],
          update_reason:
            mode === "unavailable"
              ? "Individual updates currently support one container per service."
              : undefined,
        };
        items[1] = {
          ...items[1],
          depends_on: [],
          updatable: true,
          seamless: false,
          container_ports: [5432],
          update_reason: "Persistent storage requires a controlled restart.",
        };
      }
      return { services: items };
    }
    case "status":
      return {
        health: "healthy",
        route: "healthy",
        busy: mode === "busy" || mode === "recovery",
        public_tls_state: "unverified",
      };
    case "domains":
      return { domains: [{ hostname: "shop.example.com", assigned: true }] };
    default:
      throw new Error("Unexpected read: " + payload.read.view);
  }
});
const report = (error: unknown) =>
  console.info("Simulated read error", String(error));
const rejectMutation = async () => {
  throw new Error("No mutations allowed");
};
function Fixture() {
  const [project, setProject] = useState(original);
  const [message, setMessage] = useState("Legacy server response");
  const [showJobs, setShowJobs] = useState(false);
  return (
    <main style={{ maxWidth: 1100, margin: "24px auto", padding: 24 }}>
      <p className="eyebrow">TEST FIXTURE · NO SERVER ACCESS</p>
      <div
        style={{ display: "flex", gap: 8, flexWrap: "wrap", marginBottom: 24 }}
      >
        {["legacy", "current", "busy", "recovery", "unavailable", "error"].map(
          (value) => (
            <button
              className="button small"
              key={value}
              onClick={() => {
                mode = value;
                setMessage(`${value} response selected; refresh Services`);
              }}
            >
              {value}
            </button>
          ),
        )}
        <button className="button small" onClick={() => setShowJobs(false)}>
          Show services
        </button>
        <button
          className="button small"
          onClick={() => {
            replaced = true;
            mode = "current";
            setProject({
              ...original,
              service_instances: {
                web: {
                  name: "dy-update-example",
                  release: {
                    ...release,
                    id: "rel-web-v2",
                    image: "example/shop:v2",
                  },
                },
              },
            });
            setMessage(
              `New service revision; ${serviceReads} reads before change`,
            );
          }}
        >
          Simulate completed update
        </button>
      </div>
      <p role="status">{message}</p>
      {showJobs ? (
        <Jobs
          jobs={mode === "recovery" ? [interrupted] : []}
          loading={false}
          preview={true}
          refresh={async () => {}}
          report={report}
        />
      ) : (
        <ProjectDetail
          project={project}
          preview={false}
          report={report}
          open={() => {
            throw new Error("No project mutations allowed");
          }}
          action={rejectMutation}
          execute={rejectMutation}
          openServerDetails={() => setMessage("Server details shortcut works")}
          jobs={mode === "recovery" ? [interrupted] : []}
          openDeployments={() => setShowJobs(true)}
        />
      )}
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
