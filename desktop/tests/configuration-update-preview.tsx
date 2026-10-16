// Local-only fixture: no credentials, Docker mutations, or VPS requests.
import { useState } from "react";
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import { Toaster } from "react-hot-toast";
import ProjectConfiguration from "../src/ProjectConfiguration";
import type { Project } from "../src/types";
import "../src/style.css";
import "../src/macos.css";

const source =
  'services:\n  web:\n    image: example/app:latest\n    environment:\n      APP_MESSAGE: ${APP_MESSAGE}\n    ports: ["3000:3000"]\n    healthcheck:\n      test: [CMD, curl, -f, http://localhost:3000/health]\n  database:\n    image: postgres:17\n    volumes: [data:/var/lib/postgresql/data]\nvolumes:\n  data: {}\n';
let portState = "available";
let portReads = 0;
mockIPC((command, payload: any) => {
  if (command === "read_api" && payload.read.kind === "port") {
    if (payload.read.count !== 3)
      throw new Error("Expected three port suggestions");
    if (portState === "error") throw new Error("HTTP_503: DOCKER_UNAVAILABLE");
    if (portState === "older server")
      throw new Error("HTTP_400: REQUEST_INVALID");
    if (portState === "exhausted")
      throw new Error("HTTP_409: PORT_POOL_EXHAUSTED");
    const start = 3016 + 3 * portReads++;
    return {
      port: start,
      ports: portState === "limited" ? [start] : [start, start + 1, start + 2],
      reserved: false,
    };
  }
  if (command === "read_api")
    return { release_id: "rel-current", compose_yaml: source, draft: true };
  if (command === "read_env" || command === "import_env") {
    return command === "import_env"
      ? "APP_MESSAGE=updated\n"
      : { release_id: "rel-current", env_file: "APP_MESSAGE=updated\n" };
  }
  if (command === "save_draft") return { saved: true };
  throw new Error(`Unexpected fixture command: ${command}`);
});
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
  slots: {},
  releases: [],
};
function Fixture() {
  const [result, setResult] = useState(
    "Ready · deployment uses the saved env draft",
  );
  return (
    <main style={{ maxWidth: 1100, margin: "24px auto", padding: 24 }}>
      <p>TEST FIXTURE · simulated configuration · no server access</p>
      <p role="status">{result}</p>
      <div style={{ display: "flex", gap: 8, marginBottom: 16 }}>
        {["available", "limited", "error", "older server", "exhausted"].map(
          (state) => (
            <button
              key={state}
              onClick={() => {
                portState = state;
                setResult(`Next port refresh: ${state}`);
              }}
            >
              Simulate {state}
            </button>
          ),
        )}
      </div>
      <ProjectConfiguration
        project={project}
        preview={false}
        onDirtyChange={() => {}}
        execute={async (input: any) => {
          if (
            input.action !== "deploy" ||
            input.data.expected_release_id !== "rel-current" ||
            input.data.env_file !== "APP_MESSAGE=updated\n" ||
            input.data.compose_yaml !== source ||
            !["seamless", "restart"].includes(input.data.mode)
          )
            throw new Error("Incorrect configuration update payload");
          setResult(
            `PASS · ${input.data.mode} deployment includes the updated env and current release guard`,
          );
        }}
      />
      <Toaster />
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
