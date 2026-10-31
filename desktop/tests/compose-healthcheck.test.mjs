import assert from "node:assert/strict";
import test from "node:test";
import { parse } from "yaml";
import {
  configureHealthcheck,
  healthcheckServices,
  suggestHealthcheck,
  validHealthcheckInterval,
} from "../src/composeHealthcheck.ts";

const source = `# production services
services:
  web:
    image: app:\${TAG}
    healthcheck:
      test: [CMD-SHELL, 'curl -f http://localhost:$$PORT/health']
      interval: 3s
      timeout: 2s
      retries: 4
      start_period: 30s
  worker:
    image: worker:latest
  database:
    image: postgres:17
`;

test("disabling checks overrides image probes and retains the configured command for re-enabling", () => {
  const before = parse(source);
  const off = configureHealthcheck(source, "web", { disabled: true });
  const after = parse(off);
  assert.deepEqual(after.services.web.healthcheck, {
    ...before.services.web.healthcheck,
    disable: true,
  });
  assert.deepEqual(after.services.worker, before.services.worker);
  assert.deepEqual(after.services.database, before.services.database);
  assert.equal(after.services.web.image, "app:${TAG}");
  assert.match(off, /# production services/);
  assert.equal(healthcheckServices(off)[0].disabled, true);
  const enabled = parse(
    configureHealthcheck(off, "web", { disabled: false, interval: "1m" }),
  );
  assert.deepEqual(enabled.services.web.healthcheck, {
    ...before.services.web.healthcheck,
    interval: "1m",
  });
});

test("an inherited image probe can be disabled or assigned an interval without inventing a probe", () => {
  const off = configureHealthcheck(source, "worker", { disabled: true });
  assert.deepEqual(parse(off).services.worker.healthcheck, { disable: true });
  const enabled = configureHealthcheck(off, "worker", {
    disabled: false,
    interval: "5m",
  });
  assert.deepEqual(parse(enabled).services.worker.healthcheck, {
    interval: "5m",
  });
  const defaults = configureHealthcheck(enabled, "worker", { disabled: false });
  assert.deepEqual(parse(defaults).services.worker.healthcheck, {});
  const none = source.replace(
    "image: worker:latest",
    "image: worker:latest\n    healthcheck:\n      test: [NONE]",
  );
  assert.equal(
    healthcheckServices(none).find((s) => s.name === "worker").disabled,
    true,
  );
  assert.deepEqual(
    parse(
      configureHealthcheck(none, "worker", { disabled: false, interval: "1m" }),
    ).services.worker.healthcheck,
    { interval: "1m" },
  );
});

test("health-based dependencies change only after an explicit startup choice", () => {
  const dependent =
    source +
    `  api:
    image: api:latest
    depends_on:
      web:
        condition: service_healthy
        restart: true
        required: false
      database:
        condition: service_healthy
`;
  assert.deepEqual(healthcheckServices(dependent)[0].dependents, ["api"]);
  assert.throws(
    () => configureHealthcheck(dependent, "web", { disabled: true }),
    /api.*startup/,
  );
  const after = parse(
    configureHealthcheck(dependent, "web", {
      disabled: true,
      waitForStart: true,
    }),
  );
  assert.deepEqual(after.services.api.depends_on, {
    web: { condition: "service_started", restart: true, required: false },
    database: { condition: "service_healthy" },
  });
});

test("editing an alias or merge override leaves shared healthcheck defaults and siblings intact", () => {
  for (const inherited of ["healthcheck: *probe", "<<: *defaults"]) {
    const aliased = `x-probe: &probe
  test: [CMD, true]
  timeout: 2s
  interval: 3s
x-defaults: &defaults
  healthcheck: *probe
services:
  web:
    image: app:latest
    ${inherited}
  sibling:
    image: app:latest
    healthcheck: *probe
`;
    const before = parse(aliased, { merge: true });
    const after = parse(
      configureHealthcheck(aliased, "web", { disabled: false, interval: "1m" }),
      { merge: true },
    );
    assert.equal(after.services.web.healthcheck.interval, "1m");
    assert.deepEqual(
      after.services.web.healthcheck.test,
      before.services.web.healthcheck.test,
    );
    assert.deepEqual(after.services.sibling, before.services.sibling);
    assert.deepEqual(after["x-probe"], before["x-probe"]);
  }
});

test("shared anchor definitions are rejected instead of changing siblings or breaking references", () => {
  for (const anchored of [
    "services:\n  web: &web\n    image: app:latest\n  sibling: *web\n",
    "services:\n  web:\n    image: app:latest\n    healthcheck: &probe\n      test: [CMD, true]\n  sibling:\n    image: app:latest\n    healthcheck: *probe\n",
    "services:\n  web:\n    image: app:latest\n    healthcheck:\n      test: &probe [CMD, true]\n  sibling:\n    image: app:latest\n    healthcheck:\n      test: *probe\n",
  ])
    assert.throws(
      () => configureHealthcheck(anchored, "web", { disabled: true }),
      /anchor/,
    );
});

test("positive Docker durations and normalized JSON work; invalid settings never edit a draft", () => {
  for (const interval of ["30s", "1m", "1m30s", "5m", "1.5s", "500ms"])
    assert.equal(validHealthcheckInterval(interval), true, interval);
  for (const interval of ["", "0s", "-1m", "1", "foo", "1m junk", "1m\n"])
    assert.equal(validHealthcheckInterval(interval), false, interval);
  assert.throws(
    () =>
      configureHealthcheck(source, "web", {
        disabled: false,
        interval: "invalid",
      }),
    /positive interval/,
  );
  assert.throws(
    () => configureHealthcheck("services: [", "web", { disabled: true }),
    /syntax/,
  );
  assert.throws(
    () => configureHealthcheck(source, "missing", { disabled: true }),
    /not defined|anchor/,
  );
  assert.equal(
    healthcheckServices(
      JSON.stringify({
        services: { web: { image: "app", healthcheck: { interval: 60e9 } } },
      }),
    )[0].interval,
    "60s",
  );
  assert.equal(suggestHealthcheck("postgres:17").interval, "1m");
});
