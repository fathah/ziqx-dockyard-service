import assert from "node:assert/strict";
import { test } from "node:test";
import { startServiceImagePolling } from "../src/serviceImagePoller.ts";

const ready = (status = "current", error) => ({
  status: "ready",
  services: [{ service: "api", status, ...(error ? { error } : {}) }],
});
const flush = async () => {
  for (let i = 0; i < 8; i++) await Promise.resolve();
};
function fixture(request, options = {}) {
  let time = 0;
  let next = 0;
  const timers = new Map();
  const published = [];
  const calls = [];
  const polling = startServiceImagePolling({
    request: async (...args) => {
      calls.push(args);
      return request(...args);
    },
    publish: (value) => published.push(value),
    schedule: (callback, delay) => {
      const id = ++next;
      timers.set(id, { callback, at: time + delay });
      return id;
    },
    cancel: (id) => timers.delete(id),
    now: () => time,
    ...options,
  });
  return {
    polling,
    calls,
    published,
    timers,
    async advance(ms) {
      time += ms;
      for (const [id, timer] of [...timers]) {
        if (timer.at <= time) {
          timers.delete(id);
          timer.callback();
        }
      }
      await flush();
    },
  };
}

test("completed metadata rechecks periodically and on focus, clearing a stale update", async () => {
  let state = "available";
  const f = fixture(async () => ready(state));
  await flush();
  assert.deepEqual(f.calls, [[false, true]]);
  assert.equal(f.published.at(-1).services[0].status, "available");
  state = "current";
  await f.advance(60_000);
  assert.deepEqual(f.calls.at(-1), [false, true]);
  assert.equal(f.published.at(-1).services[0].status, "current");
  f.polling.refresh();
  assert.equal(f.published.at(-1).status, "checking");
  await flush();
  assert.equal(f.calls.length, 3);
  f.polling.stop();
  assert.equal(f.timers.size, 0);
});

test("active checks poll without restarting and deduplicate focus events", async () => {
  let resolve;
  const pending = new Promise((done) => {
    resolve = done;
  });
  let first = true;
  const f = fixture(() => {
    if (first) {
      first = false;
      return pending;
    }
    return ready();
  });
  f.polling.refresh();
  f.polling.refresh();
  assert.equal(f.calls.length, 1);
  resolve({ status: "checking" });
  await flush();
  await f.advance(1500);
  assert.deepEqual(f.calls, [
    [false, true],
    [false, false],
  ]);
  assert.equal(f.published.at(-1).services[0].status, "current");
  f.polling.stop();
});

test("preparation keeps the reviewed digest and never periodically repulls", async () => {
  let first = true;
  const f = fixture(
    async () => {
      if (first) {
        first = false;
        return { status: "pulling" };
      }
      return { status: "ready", image: "sha256:" + "2".repeat(64) };
    },
    { prepare: true, retryPreparation: true },
  );
  await flush();
  await f.advance(1500);
  assert.deepEqual(f.calls, [
    [true, false],
    [false, false],
  ]);
  await f.advance(600_000);
  assert.equal(f.calls.length, 2);
  assert.equal(f.timers.size, 0);
  assert.equal(f.published.at(-1).image, "sha256:" + "2".repeat(64));
  f.polling.stop();
});

test("hidden views wait for visibility; a stopped observer ignores late results", async () => {
  let visible = false;
  let resolve;
  const pending = new Promise((done) => {
    resolve = done;
  });
  const f = fixture(() => pending, { visible: () => visible });
  assert.equal(f.calls.length, 0);
  await f.advance(60_000);
  assert.equal(f.calls.length, 0);
  visible = true;
  f.polling.refresh();
  assert.equal(f.calls.length, 1);
  f.polling.stop();
  resolve(ready("available"));
  await flush();
  assert.deepEqual(f.published, [{ status: "checking" }]);
  assert.equal(f.timers.size, 0);
});

test("container replacement retries soon; failures never claim an image is current", async () => {
  let changed = true;
  const f = fixture(async () => {
    if (changed) return ready("unknown", "SERVICE_IMAGE_CHANGED");
    throw new Error("REGISTRY_CHECK_FAILED");
  });
  await flush();
  changed = false;
  await f.advance(3000);
  assert.equal(f.calls.length, 2);
  assert.equal(f.published.at(-1).status, "failed");
  assert.match(f.published.at(-1).error, /REGISTRY_CHECK_FAILED/);
  f.polling.stop();
});

test("older agents fall back once without changing preparation requests", async () => {
  const f = fixture(async (_retry, refresh) => {
    if (refresh) throw new Error("REQUEST_INVALID");
    return ready();
  });
  await flush();
  assert.deepEqual(f.calls, [
    [false, true],
    [false, false],
  ]);
  await f.advance(60_000);
  assert.deepEqual(f.calls.at(-1), [false, false]);
  assert.equal(f.calls.length, 3);
  f.polling.stop();
});
