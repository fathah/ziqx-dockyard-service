// Local-only fixture: every native request is simulated, with no VPS access.
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import { ProjectDetail } from "../src/App";
import type { Project, Service } from "../src/types";
import "../src/style.css";
const release = {
  id: "rel-current",
  image: "",
  environment_revision: "env-current",
  created_at: "2026-10-11T00:00:00Z",
};
const project: Project = {
  id: "demo-production",
  app_id: "demo",
  mode: "compose",
  environment: "production",
  state: "running",
  template_id: "",
  domains: ["app.example.com"],
  route_service: "web",
  route_port: 3000,
  zerodowntime: false,
  blue_port: 3001,
  active_slot: "blue",
  slots: { blue: release },
  releases: [release],
};
const services: Service[] = [
  {
    name: "web",
    image: "ghcr.io/example/web:latest",
    slot: "blue",
    template_id: "",
    environment_revision: "env-current",
    updatable: true,
    seamless: true,
    container_ports: [3000],
    host_ports: [3001],
    depends_on: [],
  },
];
let checks = 0;
mockIPC((command, payload: any) => {
  if (command === "project_logo_read") return null;
  if (command === "read_api") {
    switch (payload.read?.view) {
      case "services":
        return { services };
      case "status":
        return {
          health: "healthy",
          route: "ok",
          busy: false,
          public_tls_state: "unverified",
        };
      case "domains":
        return [{ hostname: "app.example.com", assigned: true }];
    }
  }
  if (command === "service_images") {
    if (payload.data.service)
      return { status: "ready", image: "sha256:" + "2".repeat(64) };
    checks++;
    const state =
      new URLSearchParams(location.search).get("state") ?? "available";
    return checks === 1
      ? { status: "checking" }
      : {
          status: "ready",
          services: [
            {
              service: "web",
              image: services[0].image,
              status: state,
              current_image: "sha256:" + "1".repeat(64),
              latest_image: "sha256:" + "2".repeat(64),
            },
          ],
        };
  }
  if (command === "check_domain_https")
    return {
      state: "verified",
      http_status: 200,
      checked_at: Date.now() / 1000,
    };
  if (command === "preview_service_update")
    return {
      prepared_image: "sha256:" + "2".repeat(64),
      review_sha256: "a".repeat(64),
      service: "web",
      mode: payload.data.mode,
      domains: project.domains,
      container_port: 3000,
      host_port: 0,
      imports_routes: false,
      dependencies_unchanged: true,
    };
  throw new Error(`Unexpected mock command: ${command}`);
});
createRoot(document.getElementById("root")!).render(
  <main style={{ padding: 32, maxWidth: 1150, margin: "auto" }}>
    <p>TEST FIXTURE · simulated GHCR · no server access</p>
    <ProjectDetail
      project={project}
      preview={false}
      open={() => {}}
      action={async () => {}}
      execute={async () => {}}
      report={console.error}
      openServerDetails={() => {}}
    />
  </main>,
);
