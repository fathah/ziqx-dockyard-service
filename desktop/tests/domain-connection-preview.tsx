// Local fixtures only: no VPS, Cloudflare, Keychain, or native mutations.
import { useState } from "react";
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import DomainConnection from "../src/DomainConnection";
import type { Project, Service } from "../src/types";
import "../src/style.css";
import "../src/macos.css";

const release = {
  id: "rel-original",
  image: "",
  environment_revision: "env-original",
  created_at: "2026-10-11T00:00:00Z",
};
const project: Project = {
  id: "developers",
  app_id: "ziqx-developers",
  environment: "production",
  mode: "compose",
  template_id: "",
  domains: ["ziqx.cc"],
  route_service: "web",
  route_port: 3000,
  blue_port: 3001,
  zerodowntime: false,
  active_slot: "blue",
  state: "running",
  slots: { blue: release },
  releases: [release],
};
const service: Service = {
  name: "api",
  slot: "blue",
  image: "api:stable",
  container_ports: [3040],
  template_id: "",
  environment_revision: "env-original",
};
let diverged = true;
let dnsApplied = false;
const params = new URLSearchParams(location.search);
if (params.has("ready")) diverged = false;
const report = () => ({
  root_ssh: true,
  update_directory: "ready",
  installed_binaries: true,
  database: true,
  service_active: true,
  recent_jobs: [],
  checks: [
    {
      id: "caddy_admin",
      label: "Private Caddy connection",
      status: "ready",
      detail: "Private socket responds.",
    },
    {
      id: "caddy_coherence",
      label: "Saved and live routes",
      status: diverged ? "failed" : "ready",
      detail: diverged
        ? "Saved and live configurations differ."
        : "Saved routes match the running Caddy configuration.",
    },
  ],
});
mockIPC((command, payload: any) => {
  switch (command) {
    case "read_api":
      return { services: [service] };
    case "provider_list":
      return [{ id: "account1", name: "Cloudflare", kind: "cloudflare" }];
    case "provider_zones":
      return {
        items: [
          {
            id: "a".repeat(32),
            name: "ziqx.net",
            status: "active",
            account: { id: "b".repeat(32), name: "Ziqx" },
          },
        ],
        total_pages: 1,
      };
    case "preview_route_setup":
      if (diverged) throw new Error("HTTP_409: STATE_DIVERGED");
      if (params.has("custom") && !payload.data.replace_existing_rules)
        throw new Error("HTTP_409: CADDY_ROUTE_RULES_REVIEW_REQUIRED");
      return {
        review_sha256: "a".repeat(64),
        domains: payload.data.domains,
        service: payload.data.service,
        container_port: payload.data.container_port,
        upstream: "127.0.0.1:3041",
        imports_routes: params.has("move"),
        existing_routes: params.has("move")
          ? [
              {
                domains: payload.data.domains,
                previous_upstream: "localhost:8080",
                remaining_domains: ["www.ziqx.net"],
                rules_preserved: !payload.data.replace_existing_rules,
              },
            ]
          : [],
        containers_unchanged: !params.has("publish"),
      };
    case "server_access_check":
      return report();
    case "server_repair_preview":
      return {
        action: payload.action,
        review_sha256: "b".repeat(64),
        changes: [
          "Validate and reload the saved Caddy configuration.",
          "Dockyard restarts briefly. Keep a private backup and verify live routes.",
        ],
        saved_domains: ["ziqx.cc"],
        live_domains: ["old.ziqx.cc"],
        changed_sections: ["Routing and application settings"],
        reloads_caddy: true,
        live_available: true,
        saved_available: true,
      };
    case "server_repair_apply":
      diverged = false;
      return report();
    case "provider_service_dns_preview":
      return {
        action: dnsApplied ? "reuse" : "create",
        name: payload.input.name,
        record_type: "A",
        server_ip: "203.0.113.25",
        proxied: payload.input.proxied,
        review_sha256: "c".repeat(64),
      };
    case "provider_service_dns_apply":
      dnsApplied = true;
      return {};
    default:
      throw new Error("No external access in this fixture");
  }
});
function Fixture() {
  const [open, setOpen] = useState(true);
  const [result, setResult] = useState("");
  return (
    <main style={{ padding: 40 }}>
      <h1>Service domain connection</h1>
      <button onClick={() => setOpen(true)}>Connect domain</button>
      <pre role="status">{result}</pre>
      {open && (
        <DomainConnection
          project={project}
          service={params.has("async") ? undefined : service}
          initialService="api"
          preview={false}
          close={() => setOpen(false)}
          execute={async (mutation) => {
            setResult(
              JSON.stringify(
                {
                  mutation,
                  dnsApplied,
                  siblingDomainPreserved: project.domains[0],
                },
                null,
                2,
              ),
            );
          }}
        />
      )}
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
