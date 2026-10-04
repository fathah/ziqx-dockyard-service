// Development-only visual fixture. Not an application entry point or part of the Mac bundle.
import React from "react";
import { createRoot } from "react-dom/client";
import { mockIPC } from "@tauri-apps/api/mocks";
import { Toaster } from "react-hot-toast";
import { ProviderDomains } from "../src/DomainProviders";
import "../src/style.css";
const zone = {
  id: "a".repeat(32),
  name: "example.com",
  status: "active",
  account: { id: "b".repeat(32), name: "Example account" },
  name_servers: ["ada.ns.cloudflare.com", "bob.ns.cloudflare.com"],
};
let providers = [
  { id: "fixture", name: "Personal Cloudflare", kind: "cloudflare" },
];
let records = [
  {
    id: "c".repeat(32),
    type: "A",
    name: "app.example.com",
    content: "192.0.2.10",
    ttl: 1,
    proxied: true,
    modified_on: "2026-09-30T12:00:00Z",
  },
  {
    id: "d".repeat(32),
    type: "TXT",
    name: "example.com",
    content: "v=spf1 -all",
    ttl: 300,
    proxied: false,
    modified_on: "2026-09-30T12:00:00Z",
  },
];
mockIPC(async (command, args: any) => {
  await new Promise((r) => setTimeout(r, 180));
  if (command === "provider_list") return providers;
  if (command === "provider_zones")
    return { items: [zone], page: 1, total_pages: 1, total: 1 };
  if (command === "provider_records")
    return { items: records, page: 1, total_pages: 1, total: records.length };
  if (command === "provider_connect") {
    const p = {
      id: args.replace ?? "new-fixture",
      name: args.name,
      kind: "cloudflare",
    };
    providers = [...providers.filter((x) => x.id !== p.id), p];
    return p;
  }
  if (command === "provider_remove") {
    providers = providers.filter((p) => p.id !== args.provider);
    return;
  }
  if (command === "provider_token_page") return;
  if (command === "provider_write") {
    const change = args.change;
    if (change.action === "delete")
      records = records.filter((r) => r.id !== change.id);
    else if (change.action === "update")
      records = records.map((r) =>
        r.id === change.id ? { ...r, ...change.record } : r,
      );
    else
      records.push({
        ...change.record,
        id: String(records.length).repeat(32),
        modified_on: new Date().toISOString(),
      });
    return;
  }
  throw new Error(`Unexpected command ${command}`);
});
function Fixture() {
  return (
    <div style={{ maxWidth: 1200, margin: "32px auto", padding: 24 }}>
      <p>TEST FIXTURE · simulated Cloudflare · no network or credentials</p>
      <ProviderDomains
        preview={false}
        serverIP="192.0.2.40"
        routes={<section className="panel">Caddy routes fixture</section>}
      />
      <Toaster position="bottom-right" />
    </div>
  );
}
createRoot(document.getElementById("root")!).render(<Fixture />);
