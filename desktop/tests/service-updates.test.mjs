import assert from "node:assert/strict";
import test from "node:test";
import {
  serviceUpdatesNeedServerUpgrade,
  serviceUpdateUnavailableReason,
} from "../src/serviceUpdateAvailability.ts";

test("older single-container servers explain how to enable replica updates", () => {
  const service = {
    updatable: false,
    seamless: false,
    container_ports: [8080],
    update_reason:
      "Individual updates currently support one container per service.",
    containers: [{ id: "first" }, { id: "second" }],
  };
  assert.equal(serviceUpdatesNeedServerUpgrade(service), true);
  assert.match(
    serviceUpdateUnavailableReason(service),
    /Server details → Updates/,
  );
  assert.doesNotMatch(serviceUpdateUnavailableReason(service), /one container/);
});

test("servers without update metadata require an upgrade", () => {
  assert.equal(serviceUpdatesNeedServerUpgrade({}), true);
  assert.match(serviceUpdateUnavailableReason({}), /Update Dockyard/);
});

test("replica-aware servers and service-specific restrictions keep their eligibility", () => {
  for (const service of [
    { updatable: true, seamless: true, containers: [{}, {}] },
    {
      updatable: true,
      seamless: false,
      update_reason: "Add a Compose healthcheck to enable seamless updates.",
    },
    {
      updatable: false,
      update_reason: "Start this project before updating individual services.",
    },
  ]) {
    assert.equal(serviceUpdatesNeedServerUpgrade(service), false);
    if (service.update_reason)
      assert.equal(
        serviceUpdateUnavailableReason(service),
        service.update_reason,
      );
  }
});
