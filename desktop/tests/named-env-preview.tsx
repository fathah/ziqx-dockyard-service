// Development-only fixture: all files and deployments stay in memory.
import { useState } from "react";
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import { Toaster } from "react-hot-toast";
import ProjectConfiguration from "../src/ProjectConfiguration";
import type { Project } from "../src/types";
import "../src/style.css";
import "../src/macos.css";

const source = `services:
  frontend:
    image: example/frontend:latest
    env_file: .env.frontend
    ports: ["3016:3000"]
  backend:
    image: example/backend:latest
    env_file: .env.backend
    ports: ["3017:8080"]
`;
let stored = {
  compose_yaml: source,
  env_file: "SHARED_TAG=latest\n",
  env_files: {
    ".env.frontend": "PUBLIC_API_URL=https://api.example.com\n",
    ".env.backend": "DATABASE_URL=postgres://backend-only\n",
  } as Record<string, string>,
};
let report: (message: string) => void = () => {};
mockIPC((command, payload: any) => {
  if (command === "read_api" && payload.read.kind === "port")
    return { ports: [3018, 3019, 3020], reserved: false };
  if (command === "read_api")
    return {
      release_id: "rel-current",
      compose_yaml: stored.compose_yaml,
      env_files_supported: true,
      env_file_names: Object.keys(stored.env_files),
      draft: true,
    };
  if (command === "read_env")
    return {
      release_id: "rel-current",
      env_file: stored.env_file,
      env_files: { ...stored.env_files },
      env_files_supported: true,
    };
  if (command === "import_env") return "IMPORTED_VALUE=selected-file-only\n";
  if (command === "save_draft") {
    if (!payload.data.env_files || payload.data.env_file === undefined)
      throw new Error("Save omitted the environment file collection");
    stored = { ...payload.data, env_files: { ...payload.data.env_files } };
    report(
      `Saved files: .env, ${Object.keys(stored.env_files).sort().join(", ")}`,
    );
    return { saved: true };
  }
  throw new Error(`Unexpected fixture command: ${command}`);
});
const project: Project = {
  id: "multi-env-staging",
  app_id: "multi-env",
  mode: "compose",
  environment: "staging",
  state: "running",
  template_id: "",
  domains: [],
  zerodowntime: false,
  blue_port: 3016,
  active_slot: "blue",
  slots: {},
  releases: [],
};
function Fixture() {
  const [message, setMessage] = useState(
    "Select .env to open the environment files",
  );
  report = setMessage;
  return (
    <main style={{ maxWidth: 1200, margin: "24px auto", padding: 24 }}>
      <p>TEST FIXTURE · named env files · no VPS access</p>
      <p role="status">{message}</p>
      <ProjectConfiguration
        project={project}
        preview={false}
        onDirtyChange={() => {}}
        execute={async (input: any) => {
          if (
            input.action !== "deploy" ||
            input.data.expected_release_id !== "rel-current"
          )
            throw new Error("Invalid fixture deployment");
          if (
            JSON.stringify(input.data.env_files) !==
              JSON.stringify(stored.env_files) ||
            input.data.env_file !== stored.env_file
          )
            throw new Error("Deployment changed files after Save");
          setMessage(
            `PASS · deployment includes .env and ${Object.keys(input.data.env_files).length} separate named files`,
          );
        }}
      />
      <Toaster />
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
