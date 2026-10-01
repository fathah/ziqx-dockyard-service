import test from "node:test";
import assert from "node:assert/strict";
import { parse } from "yaml";
import { readFileSync } from "node:fs";
import {
  lintCompose,
  formatCompose,
  composeCompletions,
  composeForEditor,
} from "../src/composeLint.ts";

test("valid YAML and JSON Compose are accepted, including unique arrays", () => {
  const model = {
    services: {
      web: { image: "nginx:alpine", cap_drop: ["ALL"], ports: ["8080:80"] },
    },
  };
  assert.deepEqual(lintCompose(JSON.stringify(model)), []);
  assert.deepEqual(
    lintCompose("services:\n  web:\n    image: nginx:alpine\n"),
    [],
  );
});
test("syntax errors and duplicate keys point inside the document", () => {
  for (const source of [
    "services: [\n",
    "services:\n  web:\n    image: nginx\n    image: redis\n",
  ]) {
    const errors = lintCompose(source).filter((p) => p.severity === "error");
    assert.ok(errors.length);
    assert.ok(
      errors.every(
        (p) => p.from >= 0 && p.to <= source.length && p.from <= p.to,
      ),
    );
  }
});
test("unknown keys are advisory and point at the offending property", () => {
  const source = "services:\n  web:\n    imag: nginx\n";
  const p = lintCompose(source).find((p) => p.message.includes("imag"));
  assert.equal(p.severity, "warning");
  assert.equal(source.slice(p.from, p.to), "imag");
});
test("interpolation and Docker merge tags never become schema blocking errors", () => {
  for (const source of [
    "services:\n  web:\n    image: nginx\n    scale: ${REPLICAS}\n",
    'services:\n  web:\n    ports: !override ["8080:80"]\n',
  ])
    assert.ok(lintCompose(source).every((p) => p.severity === "warning"));
});
test("anchors and extension fields remain valid", () => {
  assert.deepEqual(
    lintCompose(
      "x-defaults: &defaults\n  image: nginx\nservices:\n  web:\n    <<: *defaults\n",
    ),
    [],
  );
  assert.ok(
    lintCompose("services: *missing").some((p) => p.severity === "error"),
  );
});
test("formatting preserves comments, interpolation, and values", () => {
  const source =
    '# web app\nservices:\n  web:\n    image: "nginx:${TAG}"\n    environment:\n      FLAG: "true"\n';
  const formatted = formatCompose(source);
  assert.ok(formatted.includes("# web app"));
  assert.ok(formatted.includes("${TAG}"));
  assert.deepEqual(parse(formatted), parse(source));
  const model = {
    services: {
      web: { image: "nginx", environment: { FLAG: "true", EMPTY: "" } },
    },
  };
  const yaml = formatCompose(JSON.stringify(model), true);
  assert.ok(!yaml.trimStart().startsWith("{"));
  assert.deepEqual(parse(yaml), model);
});
test("saved Docker JSON displays as YAML without changing Compose values", () => {
  const model = {
    name: "coorgdaymart",
    services: {
      web: {
        image: "example:${TAG}",
        command: null,
        ports: [{ target: 3000, published: "8080", protocol: "tcp" }],
        environment: {
          FLAG: "true",
          COUNT: "0123",
          EMPTY: "",
          VALUE: "$${LITERAL}",
          MULTILINE: "first\nsecond\n",
        },
        depends_on: {
          database: { condition: "service_healthy", required: true },
        },
      },
    },
    networks: { default: { name: "coorgdaymart_default", ipam: {} } },
  };
  const yaml = composeForEditor(JSON.stringify(model, null, 2));
  assert.ok(yaml.startsWith("name: coorgdaymart\n"));
  assert.ok(!yaml.trimStart().startsWith("{"));
  assert.deepEqual(parse(yaml), model);
});
test("editor normalization preserves original YAML and malformed input", () => {
  for (const source of [
    "# keep comment\nx-app: &app\n  image: nginx:${TAG}\nservices:\n  web:\n    <<: *app\n",
    'services:\n  web:\n    ports: !override ["8080:80"]\n',
    '{"services":',
    "",
  ])
    assert.equal(composeForEditor(source), source);
});
test("schema completions follow root, services, and nested healthcheck", () => {
  for (const [source, expected] of [
    ["", "services"],
    ["services:\n  web:\n    im", "image"],
    ["services:\n  web:\n    healthcheck:\n      in", "interval"],
  ])
    assert.ok(
      composeCompletions(source, source.length).some(
        (p) => p.label === expected,
      ),
      expected,
    );
});
test("standalone schema validator works without runtime code generation", () => {
  const code = readFileSync(
    new URL("../src/generated/composeValidator.js", import.meta.url),
    "utf8",
  );
  assert.ok(!/new Function\(|\beval\(|\brequire\(/.test(code));
});
