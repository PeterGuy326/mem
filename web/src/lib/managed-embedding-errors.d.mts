export interface ManagedEmbeddingAPIError {
  status?: number;
  error?: string;
  hint?: string;
}

export interface ManagedEmbeddingErrorPresentation {
  kind:
    | 'session_expired'
    | 'workspace_forbidden'
    | 'plan_required'
    | 'quota_exhausted'
    | 'provider_unavailable'
    | 'provider_timeout'
    | 'unknown';
  title: string;
  message: string;
  hint?: string;
  action:
    | 'sign_in'
    | 'switch_workspace'
    | 'view_membership'
    | 'wait_for_reset'
    | 'retry'
    | 'review_request';
  retryable: boolean;
}

export function managedEmbeddingErrorPresentation(
  error: ManagedEmbeddingAPIError,
): ManagedEmbeddingErrorPresentation;
