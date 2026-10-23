import assert from "node:assert/strict";
import test from "node:test";
import { createServer } from "vite";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

// Render the review component used after the VPS preview response.
test("renders Caddy transfer reviews when no domains remain in the original site", async () => {
  const server = await createServer({
    server: { middlewareMode: true, hmr: false, ws: false },
    appType: "custom",
  });
  try {
    const { ExistingRouteReviews } = await server.ssrLoadModule(
      "/src/RouteSetup.tsx",
    );
    for (const remaining of [null, undefined, []]) {
      const review = {
        service: "ziqxcoreweb",
        container_port: 3000,
        existing_routes: [
          {
            domains: ["ziqx.cc"],
            remaining_domains: remaining,
            rules_preserved: false,
          },
        ],
      };
      const html = renderToStaticMarkup(
        React.createElement(ExistingRouteReviews, { review }),
      );
      assert.match(html, /ziqx.cc/);
      assert.match(html, /standard service route/);
      assert.doesNotMatch(html, /Stays in the main Caddyfile/);
    }
    const html = renderToStaticMarkup(
      React.createElement(ExistingRouteReviews, {
        review: {
          service: "web",
          container_port: 3000,
          existing_routes: [
            {
              domains: ["example.com"],
              remaining_domains: ["www.example.com"],
              rules_preserved: true,
            },
          ],
        },
      }),
    );
    assert.match(html, /Stays in the main Caddyfile/);
    assert.match(html, /www.example.com/);
    assert.match(html, /only the proxy destination changes/);
  } finally {
    await server.close();
  }
});

test("stopped domain setup remains editable and unlink works without live services", async () => {
  const server = await createServer({
    server: { middlewareMode: true, hmr: false, ws: false },
    appType: "custom",
  });
  try {
    const { default: RouteSetup } = await server.ssrLoadModule(
      "/src/RouteSetup.tsx",
    );
    const { domainTarget, serviceDomains, routeReleaseId } =
      await server.ssrLoadModule("/src/domainRoutes.ts");
    const release = { id: "active-release" };
    const project = {
      id: "demo",
      app_id: "demo",
      mode: "compose",
      state: "stopped",
      active_slot: "blue",
      slots: { blue: release },
      releases: [release],
      domains: ["api.example.com", "metrics.example.com"],
      service_routes: [
        {
          service: "api",
          container_port: 3000,
          host_port: 0,
          managed: true,
          domains: ["api.example.com"],
        },
        {
          service: "api",
          container_port: 9090,
          host_port: 0,
          managed: true,
          domains: ["metrics.example.com"],
        },
      ],
    };
    const props = {
      project,
      services: [{ name: "api", slot: "blue", container_ports: [3000] }],
      initialService: "api",
      loading: false,
      unavailable: false,
      blocked: false,
      preview: false,
      execute: async () => {},
      onClose: () => {},
      refresh: () => {},
    };
    const html = renderToStaticMarkup(React.createElement(RouteSetup, props));
    assert.match(html, /Pending activation/);
    assert.match(html, /placeholder="app.example.com"/);
    assert.doesNotMatch(html, /Start demo to connect/);
    assert.match(html, />Review</);
    const unlink = renderToStaticMarkup(
      React.createElement(RouteSetup, {
        ...props,
        services: [],
        unlink: true,
        initialDomains: ["metrics.example.com"],
      }),
    );
    assert.match(unlink, /Unlink metrics.example.com from api/);
    assert.match(unlink, /DNS records and containers will stay/);
    assert.doesNotMatch(unlink, /Domain source/);
    assert.match(unlink, />Review</);
    assert.equal(
      domainTarget(project, "metrics.example.com").container_port,
      9090,
    );
    assert.deepEqual(serviceDomains(project, "api"), project.domains);
    assert.equal(
      routeReleaseId({
        ...project,
        slots: { blue: { id: "failed-candidate" } },
      }),
      "active-release",
    );
  } finally {
    await server.close();
  }
});
