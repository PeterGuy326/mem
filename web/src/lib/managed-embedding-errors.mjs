/**
 * Stable presentation mapping for managed-embedding API failures.
 * Kept as a dependency-free module so the exact mapping used by React can be
 * exercised by Node without a browser or a second implementation.
 */
export function managedEmbeddingErrorPresentation(error) {
  const status = Number(error?.status ?? 0);
  switch (status) {
    case 401:
      return {
        kind: 'session_expired',
        title: 'Sign in required',
        message: 'Your session is no longer valid. Sign in again to continue.',
        action: 'sign_in',
        retryable: false,
      };
    case 403:
      return {
        kind: 'workspace_forbidden',
        title: 'Workspace access required',
        message: 'This account or Agent token cannot use the selected workspace.',
        action: 'switch_workspace',
        retryable: false,
      };
    case 402:
      return {
        kind: 'plan_required',
        title: 'Membership required',
        message: 'Managed embeddings are included with an active workspace membership.',
        action: 'view_membership',
        retryable: false,
      };
    case 429:
      return {
        kind: 'quota_exhausted',
        title: 'Embedding quota reached',
        message: 'The workspace quota is exhausted. Retry after the displayed reset time.',
        action: 'wait_for_reset',
        retryable: true,
      };
    case 502:
      return {
        kind: 'provider_unavailable',
        title: 'Embedding service unavailable',
        message: 'The managed provider failed safely. No automatic provider fallback was used.',
        action: 'retry',
        retryable: true,
      };
    case 504:
      return {
        kind: 'provider_timeout',
        title: 'Embedding request timed out',
        message:
          'Do not retry automatically. Check the usage or request status, or contact an administrator before taking further action.',
        action: 'review_request',
        retryable: false,
      };
    default: {
      const hint =
        typeof error?.hint === 'string' && error.hint.length > 0
          ? error.hint
          : undefined;
      return {
        kind: 'unknown',
        title: 'Could not load embedding status',
        message: hint ?? 'Try again or use a local/BYOM provider.',
        hint,
        action: 'retry',
        retryable: true,
      };
    }
  }
}
