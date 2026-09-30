import assert from 'node:assert/strict';
import test from 'node:test';
import { projectIdentity } from '../src/projectNaming.ts';

const existing = (app_id, environment = 'production', id = `${app_id}-${environment}`) => ({ app_id, environment, id });

test('names become safe folder IDs and empty names cannot submit', () => {
  assert.equal(projectIdentity('  ', 'production', []), null);
  assert.equal(projectIdentity('Payments', '', []), null);
  assert.equal(projectIdentity('  Café Payments! ', 'production', []).id, 'cafe-payments-production');
  for (const name of ['123 app', '日本語', '../escape', 'A'.repeat(120)]) {
    const result = projectIdentity(name, 'development', []);
    assert.match(result.id, /^[a-z][a-z0-9-]{0,47}$/);
    assert.match(result.app_id, /^[a-z][a-z0-9-]{0,47}$/);
  }
});
test('same name groups different environments under one application', () => {
  const result = projectIdentity('Payments', 'staging', [existing('payments')]);
  assert.equal(result.app_id, 'payments');
  assert.equal(result.id, 'payments-staging');
  assert.equal(result.adjusted, false);
});
test('duplicate names skip occupied application suffixes in all environments', () => {
  const result = projectIdentity('PAYMENTS', 'production', [existing('payments'), existing('payments-2', 'staging')]);
  assert.equal(result.app_id, 'payments-3');
  assert.equal(result.adjusted, true);
});
test('generated IDs never reuse managed or observed folders', () => {
  const result = projectIdentity('Payments', 'production', [existing('other', 'staging', 'payments-production')], ['payments-production-2']);
  assert.equal(result.id, 'payments-production-3');
  assert.equal(projectIdentity('Payments', 'production', [], ['payments']).app_id, 'payments-2');
});
test('long duplicate names retain room for suffixes and environment', () => {
  const name = 'a'.repeat(120);
  const first = projectIdentity(name, 'development', []);
  const second = projectIdentity(name, 'development', [existing(first.app_id, 'development')]);
  assert.notEqual(first.app_id, second.app_id);
  assert.match(second.id, /^[a-z][a-z0-9-]{0,47}$/);
});
