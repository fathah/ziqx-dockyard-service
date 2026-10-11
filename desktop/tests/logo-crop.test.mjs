import assert from "node:assert/strict";
import test from "node:test";
import { cropGeometry } from "../src/logoCrop.ts";
test("square crop remains centered at each zoom for portrait and landscape images", () => {
  for (const [w, h] of [
    [1400, 1000],
    [600, 1200],
    [64, 64],
    [10000, 2000],
  ])
    for (const zoom of [1, 2, 4]) {
      const c = cropGeometry(w, h, zoom);
      assert(
        c.sx >= 0 && c.sy >= 0 && c.sx + c.side <= w && c.sy + c.side <= h,
      );
      assert.equal(c.sx + c.side / 2, w / 2);
      assert.equal(c.sy + c.side / 2, h / 2);
      assert(c.output <= 900 && c.output <= c.side);
    }
});
test("caps images at 900px without enlarging smaller logos", () => {
  assert.equal(cropGeometry(1400, 1000, 1).output, 900);
  assert.equal(cropGeometry(64, 64, 1).output, 64);
});
