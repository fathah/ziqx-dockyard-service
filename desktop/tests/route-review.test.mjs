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
