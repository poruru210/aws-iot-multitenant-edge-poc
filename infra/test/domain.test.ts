import test from 'node:test';
import assert from 'node:assert/strict';
import { allowed, validId, type Ticket, type HookInput } from '../functions/domain';
const hash = 'a'.repeat(64);
const arn = 'arn:aws:iot:ap-northeast-1:111122223333:provisioningtemplate/test';
const ticket: Ticket = { tenantId: 'tenant-a', thingName: 'edge-001', publicKeyHash: hash, claimId: 'claim1', expiresAt: 200, status: 'ISSUED' };
const event: HookInput = { claimCertificateId: 'claim1', certificateId: 'b'.repeat(64), certificatePem: '', templateArn: arn };
test('valid ticket; attacker tenant parameters do not influence authorization', () => {
  assert.equal(allowed(ticket, {...event, parameters: {TenantId: 'tenant-b'}}, hash, 100, arn), true);
});
test('unknown, expired, revoked, wrong key, wrong claim and wrong template fail closed', () => {
  assert.equal(allowed(undefined, event, hash, 100, arn), false);
  for (const bad of [{...ticket, expiresAt: 100}, {...ticket, status: 'REVOKED'}, {...ticket, publicKeyHash: 'c'.repeat(64)}, {...ticket, claimId: 'other'}, {...ticket, tenantId: '../b'}]) {
    assert.equal(allowed(bad, event, hash, 100, arn), false);
  }
  assert.equal(allowed(ticket, {...event, templateArn: 'other'}, hash, 100, arn), false);
});
test('only the same certificate can retry', () => {
  const bound = {...ticket, status: 'APPROVED', certificateId: event.certificateId};
  assert.equal(allowed(bound, event, hash, 100, arn), true);
  assert.equal(allowed(bound, {...event, certificateId: 'd'.repeat(64)}, hash, 100, arn), false);
});
test('identifiers cannot inject IAM wildcards or S3 path segments', () => {
  for (const id of ['', '*', '?', 'a/b', '../b', '${aws:username}', 'a\n']) assert.equal(validId(id), false);
  for (const id of ['tenant-a', 'edge_001']) assert.equal(validId(id), true);
});
