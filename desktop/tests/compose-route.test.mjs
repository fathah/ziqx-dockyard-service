import assert from 'node:assert/strict';
import test from 'node:test';
import { detectComposeRoutes, suggestComposeRoute } from '../src/composeRoute.ts';

const suggest = (source, env = '', name, port) => suggestComposeRoute(detectComposeRoutes(source, env), name, port);

test('uses the internal port, including IPv6 and long port syntax', () => {
  const source = `services:
  frontend:
    image: example
    ports: ["[::1]:8080:3000", {target: 3000, published: "9000"}]
  database:
    image: postgres
`;
  assert.deepEqual(suggest(source), {service: 'frontend', port: '3000'});
});
test('resolves numeric dotenv variables, quoted values and Compose defaults', () => {
  const source = 'services:\n  api:\n    ports: ["${HOST_PORT:-8080}:${APP_PORT:-3000}"]\n';
  assert.equal(suggest(source).port, '3000');
  assert.equal(suggest(source, 'APP_PORT="4000" # comment\n').port, '4000');
  assert.equal(suggest(source, "export APP_PORT='5000'\n").port, '5000');
  assert.equal(suggest(source, 'APP_PORT=\n').port, '3000');
  assert.equal(suggest(source.replace('APP_PORT:-', 'APP_PORT-'), 'APP_PORT=\n').port, '');
});
test('requires a choice for multiple services or ports without a saved route', () => {
  const source = 'services:\n  api:\n    expose: [3000]\n  admin:\n    expose: [8080]\n';
  assert.deepEqual(suggest(source), {service: '', port: ''});
  assert.deepEqual(suggest(source, '', 'admin', 8080), {service: 'admin', port: '8080'});
  assert.equal(suggest('services:\n  api:\n    expose: [3000, 9090]\n').port, '');
});
test('respects a saved route only while it still matches the draft', () => {
  const source = 'services:\n  frontend:\n    ports: ["8000:3000"]\n';
  assert.deepEqual(suggest(source, '', 'web', 80), {service: 'frontend', port: '3000'});
  assert.deepEqual(suggest(source, '', 'frontend', 80), {service: 'frontend', port: '3000'});
  assert.deepEqual(suggest('services:\n  frontend:\n    image: example\n', '', 'frontend', 3000), {service: 'frontend', port: '3000'});
});
test('supports YAML merges and expose, ignores UDP and does not guess ranges or unknown variables', () => {
  const source = 'x-app: &app\n  expose: [3000]\nservices:\n  api:\n    <<: *app\n    ports: ["53:53/udp"]\n';
  assert.equal(suggest(source).port, '3000');
  for (const value of ['8000-8010', '${UNKNOWN}', '$$PORT', '0', '65536']) {
    assert.equal(suggest(`services:\n  api:\n    expose: ["${value}"]\n`).port, '');
  }
});
test('malformed files and secret values never become suggestions or diagnostics', () => {
  assert.ok(detectComposeRoutes('services: [', '').issue);
  const result = detectComposeRoutes('services:\n  api:\n    expose: ["${PORT}"]\n', 'PORT=private-value\n');
  assert.equal(suggestComposeRoute(result).port, '');
  assert.equal(JSON.stringify(result).includes('private-value'), false);
  assert.equal(detectComposeRoutes('services:\n  api: {}\n  api: {}\n', '').services.length, 0);
});
