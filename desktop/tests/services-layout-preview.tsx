// Local design fixture. All reads and actions are simulated; no VPS access.
import { useState } from "react";
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import { ProjectDetail } from "../src/App";
import type { Project, Service } from "../src/types";
import "../src/style.css";
import "../src/macos.css";

const release = {
  id: "rel-preview",
  image: "",
  environment_revision: "env-preview",
  created_at: "2026-10-30T00:00:00Z",
};
const project: Project = {
  id: "tanseek-production",
  app_id: "tanseek",
  environment: "production",
  mode: "compose",
  template_id: "",
  domains: ["tanseek.example.com"],
  zerodowntime: false,
  route_service: "loadbalancer",
  route_port: 8080,
  blue_port: 32861,
  active_slot: "blue",
  state: "running",
  service_routes: [
    {
      service: "frontend",
      container_port: 3000,
      host_port: 32862,
      domains: ["tanseek.example.com"],
    },
  ],
  slots: { blue: release },
  releases: [release],
};
const service = (
  name: string,
  image: string,
  extra: Partial<Service> = {},
): Service => ({
  name,
  image,
  slot: "blue",
  template_id: "",
  environment_revision: "env-preview",
  host_ports: [],
  depends_on: [],
  updatable: true,
  seamless: false,
  containers: [
    {
      id: name.padEnd(64, "a"),
      name: `dy-vps-01-tanseek-production-blue-${name}-1`,
    },
  ],
  ...extra,
});
const services = [
  service("api", "ghcr.io/fathah/tanseek-backend:latest", {
    seamless: true,
    container_ports: [8080],
    load_balancer: "loadbalancer",
    depends_on: ["db", "dragonfly", "migrate"],
    containers: [
      {
        id: "a3f9c21e8b04".padEnd(64, "a"),
        name: "dy-vps-01-tanseek-production-blue-api-1",
      },
      {
        id: "7c1d0e5fa912".padEnd(64, "b"),
        name: "dy-vps-01-tanseek-production-blue-api-2",
      },
    ],
  }),
  service("db", "postgres:17-alpine", {
    update_reason: "Persistent storage requires a controlled restart.",
  }),
  service("dragonfly", "docker.dragonflydb.io/dragonflydb/dragonfly:v2.0.0", {
    update_reason: "Persistent storage requires a controlled restart.",
  }),
  service("frontend", "ghcr.io/fathah/tanseek:latest", {
    seamless: true,
    container_ports: [3000],
    depends_on: ["api"],
  }),
  service(
    "loadbalancer",
    "haproxy:3.2.23-alpine@sha256:5961c68bc8a81c5124d0a98ab20f81b74717d6afe596e805977d9cf84c126222",
    { host_ports: [32861, 32862], depends_on: ["api"] },
  ),
  service("migrate", "ghcr.io/fathah/tanseek-backend:latest", {
    containers: [],
    updatable: false,
    update_reason: "Start at least one replica before updating this service.",
  }),
];
mockIPC((command, payload: any) => {
  if (command === "service_images")
    return {
      status: "ready",
      image: payload.data.service ? "sha256:" + "c".repeat(64) : undefined,
      services: services.map((item) => ({
        service: item.name,
        image: item.image,
        status: item.name === "frontend" ? "available" : "current",
      })),
    };
  if (command === "preview_service_update") {
    const selected = services.find(
      (item) => item.name === payload.data.service,
    )!;
    return {
      review_sha256: "a".repeat(64),
      prepared_image: "sha256:" + "c".repeat(64),
      service: selected.name,
      replicas: selected.containers?.length ?? 1,
      load_balancer: selected.load_balancer,
      mode: payload.data.mode,
      domains: selected.name === "frontend" ? project.domains : [],
      container_port: payload.data.container_port ?? 0,
      host_port: 0,
      dependencies_unchanged: true,
      imports_routes: false,
    };
  }
  if (command !== "read_api")
    throw new Error("Unexpected fixture request: " + command);
  if (payload.read.kind === "logs")
    return {
      logs: `${payload.read.service} | Sample logs\n`,
      truncated: false,
    };
  if (payload.read.view === "services") return { services };
  if (payload.read.view === "status")
    return {
      health: "healthy",
      route: "healthy",
      busy: false,
      public_tls_state: "unverified",
    };
  if (payload.read.view === "domains")
    return { domains: [{ hostname: "tanseek.example.com", assigned: true }] };
  throw new Error("Unexpected fixture read");
});
function Fixture() {
  const [message, setMessage] = useState("");
  return (
    <main style={{ maxWidth: 1500, margin: "0 auto", padding: "34px 36px" }}>
      <ProjectDetail
        project={project}
        initialTab="services"
        preview={false}
        report={(error) => setMessage(String(error))}
        open={() => setMessage("Preview only — no project changes")}
        action={async () => {
          throw new Error("No server writes in this preview");
        }}
        execute={async () => setMessage("Update simulated — no server writes")}
        openServerDetails={() => setMessage("Server details preview")}
        openShell={(selected) =>
          setMessage(`Terminal selected: ${selected.name}`)
        }
      />
      {message && (
        <p role="status" style={{ marginTop: 20 }}>
          {message}
        </p>
      )}
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
