import assert from 'node:assert/strict';

import { managedEmbeddingErrorPresentation } from './src/lib/managed-embedding-errors.mjs';

const cases = [
  [401, 'session_expired', 'sign_in', false],
  [403, 'workspace_forbidden', 'switch_workspace', false],
  [402, 'plan_required', 'view_membership', false],
  [429, 'quota_exhausted', 'wait_for_reset', true],
  [502, 'provider_unavailable', 'retry', true],
  [504, 'provider_timeout', 'review_request', false],
];

for (const [status, kind, action, retryable] of cases) {
  const result = managedEmbeddingErrorPresentation({ status, error: 'upstream-secret-body' });
  assert.equal(result.kind, kind, `status ${status} kind`);
  assert.equal(result.action, action, `status ${status} action`);
  assert.equal(result.retryable, retryable, `status ${status} retryability`);
  assert.ok(!result.message.includes('upstream-secret-body'), `status ${status} leaked API body`);
}

const timeout = managedEmbeddingErrorPresentation({ status: 504 });
assert.match(timeout.message, /Do not retry automatically/);
assert.doesNotMatch(timeout.message, /new (request|key)/i);

const unknown = managedEmbeddingErrorPresentation({ status: 418, hint: 'safe hint' });
assert.equal(unknown.kind, 'unknown');
assert.equal(unknown.message, 'safe hint');

console.log('PASS: managed embedding status mapping (401/403/402/429/502/504)');
