// Development-only fixture. Every native request is simulated; no VPS access.
import React, { useState } from "react";
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import ServiceUpdate from "../src/ServiceUpdate";
import type { Project, Service } from "../src/types";
import "../src/style.css";

const release = {
  id: "rel-original",
  image: "",
  environment_revision: "env-original",
  created_at: "2026-10-01T00:00:00Z",
};
const project: Project = {
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
const services: Service[] = [
  {
    name: "web",
    image: "example/shop:latest",
    slot: "blue",
    template_id: "",
    environment_revision: "env-original",
    updatable: true,
    seamless: true,
    container_ports: [3000, 3001],
  },
  {
    name: "postgres",
    image: "postgres:17",
    slot: "blue",
    template_id: "",
    environment_revision: "env-original",
    updatable: true,
    seamless: false,
    container_ports: [5432],
    update_reason: "Persistent storage requires a controlled restart.",
  },
];
mockIPC((command, payload: any) => {
  if (command !== "preview_service_update")
    throw new Error("Unexpected native operation: " + command);
  const { data } = payload;
  if (
    payload.project !== project.id ||
    data.expected_release_id !== release.id ||
    "env_file" in data ||
    "compose_yaml" in data
  )
    throw new Error("Invalid service review");
  if (data.service === "postgres" && data.mode !== "restart")
    throw new Error("Database must restart only itself");
  if (
    new URLSearchParams(location.search).get("caddy") === "unavailable" &&
    data.mode === "seamless"
  )
    throw new Error("HTTP_503: CADDY_UNAVAILABLE");
  return {
    review_sha256: "a".repeat(64),
    service: data.service,
    mode: data.mode,
    domains: project.domains,
    container_port: data.container_port ?? 0,
    host_port: data.mode === "seamless" ? 4104 : 0,
    imports_routes: false,
    dependencies_unchanged: true,
  };
});
function Fixture() {
  const [selected, setSelected] = useState<Service>();
  const [result, setResult] = useState("No updates submitted");
  return (
    <main style={{ maxWidth: 960, margin: "48px auto", padding: 24 }}>
      <p className="eyebrow">TEST FIXTURE · NO SERVER ACCESS</p>
      <h1>
        shop <span className="tag green">Production</span>
      </h1>
      <section className="panel">
        <div className="section-heading">
          <h2>Services</h2>
          <span className="tag">2 services</span>
        </div>
        <p className="muted">
          Update one service at a time. Its dependencies keep running.
        </p>
        {services.map((service) => (
          <div className="service-row" key={service.name}>
            <div>
              <strong>{service.name}</strong>
              <p className="mono">{service.image}</p>
              <small className="service-update-hint">
                {service.seamless
                  ? "Seamless updates available"
                  : "Controlled restart"}
              </small>
            </div>
            <button
              className="button small"
              onClick={() => setSelected(service)}
            >
              Pull & update {service.name}
            </button>
          </div>
        ))}
      </section>
      <p role="status">{result}</p>
      {selected && (
        <ServiceUpdate
          key={selected.name}
          project={project}
          service={selected}
          preview={false}
          onClose={() => setSelected(undefined)}
          execute={async (input: any) => {
            if (
              input.action !== "service_update" ||
              input.data.service !== selected.name ||
              input.data.review_sha256 !== "a".repeat(64)
            )
              throw new Error("Wrong service mutation");
            setResult(
              `Simulated update: ${input.data.service} · ${input.data.mode} · other services unchanged`,
            );
          }}
        />
      )}
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
