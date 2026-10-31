// Local-only integration fixture: all reads, saves and deployments stay in memory.
import { useState } from "react";
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import { Toaster } from "react-hot-toast";
import { parse } from "yaml";
import ProjectConfiguration from "../src/ProjectConfiguration";
import type { Project } from "../src/types";
import "../src/style.css";
import "../src/macos.css";

let compose = `# Service configuration
services:
  worker:
    image: example/worker:latest
    healthcheck:
      test: [CMD, /app/worker-health]
      interval: 3s
      timeout: 2s
  web:
    image: example/web:latest
    healthcheck:
      test: [CMD, /app/health]
      interval: 10s
    depends_on:
      database:
        condition: service_healthy
        restart: true
  database:
    image: postgres:17
    healthcheck:
      test: [CMD, pg_isready]
      interval: 10s
`;
let report = (_message: string) => {};
function summary(source: string) {
  const { services } = parse(source);
  return Object.entries(services)
    .map(
      ([name, raw]: [string, any]) =>
        `${name}: ${raw.healthcheck?.disable ? "Off" : raw.healthcheck?.interval || "Defaults"}`,
    )
    .join(" · ");
}
mockIPC((command, payload: any) => {
  if (command === "read_api")
    return { release_id: "rel-current", compose_yaml: compose };
  if (command === "read_env")
    return { release_id: "rel-current", env_file: "TOKEN=fixture-only\n" };
  if (command === "save_draft") {
    compose = payload.data.compose_yaml;
    report(`Saved draft · ${summary(compose)}`);
    return { saved: true };
  }
  throw new Error(`Unexpected fixture command: ${command}`);
});
const project: Project = {
  id: "healthchecks-production",
  app_id: "healthchecks",
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
  slots: {},
  releases: [],
};
function Fixture() {
  const [message, setMessage] = useState("Ready · no VPS access");
  report = setMessage;
  return (
    <main style={{ maxWidth: 1100, margin: "24px auto", padding: 24 }}>
      <p>TEST FIXTURE · optional Docker health checks · no server access</p>
      <p role="status">{message}</p>
      <ProjectConfiguration
        project={project}
        preview={false}
        onDirtyChange={() => {}}
        execute={async (input: any) => {
          if (
            input.action !== "deploy" ||
            input.data.expected_release_id !== "rel-current" ||
            input.data.env_file !== "TOKEN=fixture-only\n"
          )
            throw new Error("Invalid configuration deployment");
          const { services } = parse(input.data.compose_yaml);
          if (
            services.worker.healthcheck.disable &&
            input.data.mode !== "restart"
          )
            throw new Error(
              "Disabling health checks must choose Controlled restart",
            );
          compose = input.data.compose_yaml;
          setMessage(
            `PASS · ${input.data.mode} deployment · ${summary(compose)}`,
          );
        }}
      />
      <Toaster />
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
