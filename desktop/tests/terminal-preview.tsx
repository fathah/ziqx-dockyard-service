// Development-only terminal fixture. No SSH, Keychain or server access.
import React, { useState } from "react";
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import TerminalPage, { type ShellRequest } from "../src/TerminalPage";
import "../src/style.css";

let output = "";
let started = false;
mockIPC((command, payload: any) => {
  switch (command) {
    case "terminal_connect":
      if (payload.remember !== true)
        throw new Error("Password must save by default");
      if (
        payload.service &&
        (payload.service.composeProject !== "dy-fixture-shop-green" ||
          payload.service.service !== "dy-update-example")
      )
        throw new Error("Wrong service target");
      if (
        new URLSearchParams(location.search).has("missing") &&
        payload.service
      )
        throw new Error(
          "No running container found for this service. Refresh Services and try again.",
        );
      return {
        id: "test-terminal",
        password_saved: true,
        container: payload.service
          ? {
              id: "a".repeat(64),
              name: "shop-web-1",
            }
          : null,
      };
    case "terminal_start":
      if (
        payload.id !== "test-terminal" ||
        payload.cols < 10 ||
        payload.rows < 5 ||
        (payload.container !== null && payload.container !== "a".repeat(64))
      )
        throw new Error("Invalid terminal start");
      started = true;
      output = `Simulated ${payload.container ? "container" : "root"} shell · password saving enabled\r\nTerminal size: ${payload.cols} × ${payload.rows}\r\n$ `;
      return;
    case "terminal_poll": {
      const data = btoa(output);
      output = "";
      return { data, closed: false, reason: null };
    }
    case "terminal_input":
      if (!started) throw new Error("Input before shell starts");
      output += atob(payload.data);
      return;
    case "terminal_resize":
      return;
    case "terminal_close":
      started = false;
      return;
    default:
      throw new Error("Unexpected native operation: " + command);
  }
});
function Fixture() {
  const [visible, setVisible] = useState(true);
  const [request, setRequest] = useState<ShellRequest | null>(null);
  return (
    <div className="app-shell terminal-layout">
      <aside className="sidebar">
        <p>TEST FIXTURE · NO SERVER ACCESS</p>
        <button onClick={() => setVisible((v) => !v)}>
          Toggle terminal visibility
        </button>
        <button
          onClick={() => {
            setVisible(true);
            setRequest({
              composeProject: "dy-fixture-shop-green",
              composeService: "dy-update-example",
              service: "web",
              nonce: Date.now(),
            });
          }}
        >
          Open web service terminal
        </button>
      </aside>
      <main className="main">
        <div className="content content-terminal">
          <TerminalPage
            visible={visible}
            native={true}
            server="192.0.2.40"
            request={request}
            report={(e) => {
              throw e;
            }}
          />
        </div>
      </main>
    </div>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
