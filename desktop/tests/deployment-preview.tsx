// Development-only fixture: exercises first deployment without a VPS or credentials.
import React, { useState } from "react";
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import ProjectConfiguration from "../src/ProjectConfiguration";
import type { Project } from "../src/types";
import "../src/style.css";

const savedJSON = new URLSearchParams(location.search).get("saved") === "json";
const savedSource = JSON.stringify(
  {
    name: "shop-staging",
    services: {
      web: { image: "nginx:${TAG}", ports: ["8080:80"], command: null },
      worker: {
        image: "busybox:latest",
        profiles: ["background"],
        command: ["sleep", "infinity"],
      },
    },
  },
  null,
  2,
);

mockIPC((command) => {
  if (command === "read_api" && savedJSON)
    return {
      release_id: "rel-saved",
      compose_yaml: savedSource,
      env_file: "TAG=alpine\n",
    };
  if (command === "import_compose")
    return "services:\n  web:\n    image: nginx:${TAG}\n  worker:\n    image: busybox:latest\n    profiles: [background]\n    command: [sleep, infinity]\n";
  if (command === "import_env") return "TAG=alpine\n";
  throw new Error(
    `Unexpected command ${command}: first deployment must not fetch a release`,
  );
});
const project: Project = {
  id: "shop-staging",
  app_id: "shop",
  environment: "staging",
  mode: "compose",
  template_id: "",
  domains: [],
  zerodowntime: false,
  blue_port: 0,
  state: savedJSON ? "running" : "awaiting_release",
  slots: {},
  releases: savedJSON
    ? [
        {
          id: "rel-saved",
          image: "nginx",
          environment_revision: "env-saved",
          created_at: "2026-10-01T00:00:00Z",
        },
      ]
    : [],
};
function Fixture() {
  const [dirty, setDirty] = useState(false);
  const [result, setResult] = useState("");
  return (
    <main style={{ maxWidth: 1000, margin: "32px auto", padding: 24 }}>
      <p>TEST FIXTURE · simulated file imports · no server access</p>
      <p role="status">
        {result ||
          (dirty
            ? "Unsaved files"
            : savedJSON
              ? "Saved files · no edits"
              : "Ready for first deployment")}
      </p>
      <ProjectConfiguration
        project={project}
        preview={false}
        onDirtyChange={setDirty}
        execute={async (input: any) => {
          if (
            input.action !== "deploy" ||
            input.project !== "shop-staging" ||
            input.data.environment !== "staging" ||
            input.data.expected_release_id !== (savedJSON ? "rel-saved" : "") ||
            input.data.env_file !== "TAG=alpine\n" ||
            !input.data.compose_yaml.includes("worker:") ||
            input.data.compose_yaml.trimStart().startsWith("{")
          )
            throw new Error(
              "First deployment payload did not match the supplied flavor and files",
            );
          setResult(
            savedJSON
              ? "PASS · saved JSON displayed and submitted as YAML with the correct release guard"
              : "PASS · first deployment includes both files, staging target and empty release guard",
          );
        }}
      />
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
