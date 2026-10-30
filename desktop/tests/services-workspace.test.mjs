import assert from "node:assert/strict";
import test from "node:test";
import { createServer } from "vite";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

test("service details retain replica identity, dependencies and update restrictions", async () => {
  const server = await createServer({
    server: { middlewareMode: true, hmr: false, ws: false },
    appType: "custom",
  });
  try {
    const { default: Workspace, serviceDisplayState } =
      await server.ssrLoadModule("/src/ServicesWorkspace.tsx");
    const project = {
      id: "demo",
      mode: "compose",
      active_slot: "blue",
      state: "running",
      domains: [],
    };
    const services = [
      {
        name: "api",
        slot: "blue",
        image: "example/api:latest",
        seamless: true,
        updatable: true,
        host_ports: [],
        depends_on: ["db"],
        containers: [
          { id: "first", name: "api-1" },
          { id: "second", name: "api-2" },
        ],
      },
      {
        name: "db",
        slot: "blue",
        image: "postgres:17",
        depends_on: [],
        containers: [{ id: "db", name: "database" }],
      },
      {
        name: "web",
        slot: "blue",
        image: "example/web:latest",
        depends_on: ["api"],
      },
    ];
    const props = {
      project,
      services,
      loading: false,
      error: false,
      selectedKey: "blue/api",
      onSelect() {},
      blockedReason: () => "",
      controlsBlocked: false,
      onRefresh() {},
      onUpdate() {},
      onConnect() {},
      onLogs() {},
      images: {
        status: "ready",
        services: [{ service: "api", status: "current" }],
      },
    };
    const html = renderToStaticMarkup(React.createElement(Workspace, props));
    assert.match(html, /api service details/);
    assert.match(html, /2 instances/);
    assert.match(html, /api-1/);
    assert.match(html, /api-2/);
    assert.match(html, /Select db service/);
    assert.match(html, /Used by/);
    assert.match(html, /Saved environment changes can still be applied/);
    const buttons = [
      ...html.matchAll(/<button\b[^>]*>[\s\S]*?<\/button>/g),
    ].map(([button]) => button);
    assert.doesNotMatch(
      buttons.find((button) =>
        button.includes('title="Pull &amp; update api"'),
      ),
      /disabled/,
    );
    const blocked = renderToStaticMarkup(
      React.createElement(Workspace, {
        ...props,
        blockedReason: () => "Operation in progress",
      }),
    );
    const update = [...blocked.matchAll(/<button\b[^>]*>[\s\S]*?<\/button>/g)]
      .map(([button]) => button)
      .find((button) => button.includes('title="Operation in progress"'));
    assert.match(update, /disabled/);
    assert.equal(
      serviceDisplayState(project, { ...services[0], containers: null }),
      "Not checked",
    );
    assert.equal(
      serviceDisplayState(project, { ...services[0], containers: [] }),
      "Not running",
    );
  } finally {
    await server.close();
  }
});
