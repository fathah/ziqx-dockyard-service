import { test } from "node:test";
import assert from "node:assert/strict";
import {
  createServerUpdateCache,
  serverUpdateIdentity,
} from "../src/serverUpdateCache.ts";

function preview() {
  return {
    source_commit: "a".repeat(40),
    candidate_version: "0.7.33",
    candidate_source: "github",
    release_check_error: null,
    installed_version: "0.7.30",
    installed_commit: "b".repeat(40),
    version_status: "update_available",
    update_available: true,
    candidate_dockyard: "a".repeat(64),
    candidate_dockyardctl: "b".repeat(64),
    installed_dockyard: "c".repeat(64),
    installed_dockyardctl: "d".repeat(64),
    jobs: {
      sha256: "e".repeat(64),
      active_count: 0,
      recovery_count: 0,
      active_jobs: [],
      recovery_jobs: [],
    },
  };
}
function deferred() {
  let resolve;
  const promise = new Promise((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

test("launch and concurrent consumers check once; navigation/unlock retain versions", async () => {
  let calls = 0;
  const next = deferred();
  const cache = createServerUpdateCache(
    () => {
      calls++;
      return next.promise;
    },
    async () => "0.7.33",
    () => 123,
  );
  const home = cache.entry("server");
  const first = home.start();
  const details = cache.entry("server");
  const concurrent = details.check();
  await Promise.resolve();
  assert.equal(calls, 1);
  next.resolve(preview());
  await Promise.all([first, concurrent]);
  assert.equal(home.snapshot(), details.snapshot());
  home.pause();
  await home.start();
  await cache.entry("server").start();
  assert.equal(calls, 1);
  assert.equal(home.snapshot().checkedAt, 123);
  assert.equal(home.snapshot().preview.installed_version, "0.7.30");
});

test("failed launch remains manual retry only and a fresh process checks again", async () => {
  let calls = 0;
  const inspect = async () => {
    calls++;
    if (calls === 1) throw new Error("offline");
    return preview();
  };
  const cache = createServerUpdateCache(inspect, async () => "0.7.33");
  const entry = cache.entry("server");
  await entry.start();
  await entry.start();
  entry.pause();
  await entry.start();
  assert.equal(calls, 1);
  assert.equal(entry.snapshot().error, "offline");
  await entry.check();
  assert.equal(calls, 2);
  assert.equal(entry.snapshot().error, "");
  const fresh = createServerUpdateCache(inspect, async () => "0.7.33");
  await fresh.entry("server").start();
  assert.equal(calls, 3);
});

test("locking invalidates a pending result without starting an automatic retry", async () => {
  const next = deferred();
  let calls = 0;
  const entry = createServerUpdateCache(
    () => {
      calls++;
      return next.promise;
    },
    async () => "0.7.33",
  ).entry("server");
  const pending = entry.start();
  entry.pause();
  next.resolve(preview());
  assert.equal(await pending, null);
  await entry.start();
  assert.equal(calls, 1);
  assert.equal(entry.snapshot().preview, null);
  assert.equal(entry.snapshot().busy, false);
});

test("server identities isolate cache, including SSH and certificate changes", async () => {
  const profile = {
    name: "VPS",
    server_id: "server",
    key_id: "key",
    actor_id: "actor",
    origin: "https://localhost",
    server_certificate_sha256: "pin",
    server_ip: "192.0.2.1",
    ssh_port: 22,
    ssh_fingerprint: "ssh-pin",
  };
  assert.equal(
    serverUpdateIdentity({ ...profile }),
    serverUpdateIdentity(profile),
  );
  const cache = createServerUpdateCache(
    async () => preview(),
    async () => "0.7.33",
  );
  const original = cache.entry(serverUpdateIdentity(profile));
  await original.start();
  for (const change of [
    { key_id: "key2" },
    { server_certificate_sha256: "pin2" },
    { server_ip: "192.0.2.2" },
    { ssh_port: 2222 },
    { ssh_fingerprint: "ssh-pin2" },
  ]) {
    const entry = cache.entry(serverUpdateIdentity({ ...profile, ...change }));
    assert.notEqual(entry, original);
    assert.equal(entry.snapshot().attempted, false);
  }
});

test("successful install updates all views immediately without another check", async () => {
  let calls = 0;
  const cache = createServerUpdateCache(
    async () => {
      calls++;
      return preview();
    },
    async () => "0.7.33",
    () => 456,
  );
  const entry = cache.entry("server");
  await entry.start();
  const details = cache.entry("server");
  assert.equal(await entry.apply(entry.snapshot().preview), true);
  assert.equal(details.snapshot().preview.installed_version, "0.7.33");
  assert.equal(details.snapshot().preview.update_available, false);
  assert.equal(details.snapshot().preview.version_status, "current");
  await details.start();
  assert.equal(calls, 1);
});

test("fresh review exposes active work; rejected apply preserves last known versions", async () => {
  let inspections = 0;
  let installs = 0;
  const entry = createServerUpdateCache(
    async () => {
      const value = preview();
      if (++inspections > 1) value.jobs.active_count = 1;
      return value;
    },
    async () => {
      installs++;
      throw new Error("Jobs changed; check again");
    },
  ).entry("server");
  const first = await entry.start();
  assert.equal(await entry.apply(first), false);
  assert.equal(installs, 1);
  assert.equal(entry.snapshot().preview.installed_version, "0.7.30");
  assert.match(entry.snapshot().error, /Jobs changed/);
  const fresh = await entry.check();
  assert.equal(fresh.jobs.active_count, 1);
  assert.equal(await entry.apply(fresh), false);
  assert.equal(installs, 1);
});

test("an approved install finishing after lock retains its verified result and prevents duplicate work", async () => {
  const next = deferred();
  let checks = 0;
  let installs = 0;
  const entry = createServerUpdateCache(
    async () => {
      checks++;
      return preview();
    },
    () => {
      installs++;
      return next.promise;
    },
  ).entry("server");
  const expected = await entry.start();
  const applying = entry.apply(expected);
  entry.pause();
  assert.equal(entry.snapshot().busy, true);
  assert.equal(await entry.check(), null);
  assert.equal(await entry.apply(expected), false);
  next.resolve("0.7.33");
  assert.equal(
    await applying,
    false,
    "a locked view should not show a success toast",
  );
  assert.equal(entry.snapshot().busy, false);
  assert.equal(entry.snapshot().preview.installed_version, "0.7.33");
  assert.equal(entry.snapshot().preview.update_available, false);
  await entry.start();
  assert.equal(checks, 1);
  assert.equal(installs, 1);
});
