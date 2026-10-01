// Development-only fixture. Reviews are simulated and deployment is disabled.
import React, { useState } from "react";
import { createRoot } from "react-dom/client";
import BlueGreenDeployment from "../src/BlueGreenDeployment";
import Accordion from "../src/Accordion";
import type { Project } from "../src/types";
import "../src/style.css";

const source = `services:
  storefront:
    image: example/storefront:latest
    ports: ["8080:\${APP_PORT:-3000}"]
    healthcheck:
      test: [CMD, curl, -f, "http://localhost:3000/health"]
`;
const project: Project = {
  id: "existing-preview",
  app_id: "coorgdaymart",
  mode: "compose",
  environment: "production",
  template_id: "",
  domains: ["coorgdaymart.com", "www.coorgdaymart.com"],
  zerodowntime: false,
  blue_port: 3000,
  state: "running",
  slots: {},
  releases: [],
};
function Fixture() {
  const [compose, setCompose] = useState(source);
  const [dotenv, setDotenv] = useState("APP_PORT=3000\n");
  return (
    <main style={{ maxWidth: 1000, margin: "32px auto", padding: 24 }}>
      <p className="muted">TEST PREVIEW · no server access</p>
      <h1 style={{ marginTop: 16 }}>coorgdaymart</h1>
      <Accordion title="Edit sample Compose and .env">
        <label>
          Compose draft
          <textarea
            aria-label="Compose draft"
            rows={10}
            value={compose}
            onChange={(e) => setCompose(e.target.value)}
          />
        </label>
        <label>
          Environment file
          <textarea
            aria-label="Environment file"
            rows={2}
            value={dotenv}
            onChange={(e) => setDotenv(e.target.value)}
          />
        </label>
        <button
          className="button"
          onClick={() =>
            setCompose(
              source +
                "  admin:\n    image: example/admin\n    expose: [8081]\n",
            )
          }
        >
          Try multiple apps
        </button>
      </Accordion>
      <section className="panel" style={{ marginTop: 24 }}>
        <BlueGreenDeployment
          project={project}
          compose={compose}
          dotenv={dotenv}
          release="rel-preview"
          preview={true}
          execute={async () => {
            throw new Error("Fixture cannot deploy");
          }}
          onAccepted={() => {}}
        />
      </section>
    </main>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
