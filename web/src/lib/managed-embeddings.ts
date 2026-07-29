import { api, ApiException } from './api';
import {
  managedEmbeddingErrorPresentation,
  type ManagedEmbeddingErrorPresentation,
} from './managed-embedding-errors.mjs';

export interface ManagedEmbeddingSummary {
  workspace_id: string;
  plan: string;
  status: string;
  qualifying: boolean;
  managed_embedding_unit_limit: number;
  managed_embedding_units_reserved: number;
  managed_embedding_units_consumed: number;
  managed_embedding_units_remaining: number;
  period_start: string;
  reset_at: string;
}

export interface EntitlementResponse {
  deployment_mode: 'private' | 'saas';
  commercial_gate: boolean;
  upgrade_required: boolean;
  plan?: string;
  status?: string;
  managed_embedding?: ManagedEmbeddingSummary;
}

export function getEntitlementSummary(): Promise<EntitlementResponse> {
  return api.get<EntitlementResponse>('/entitlements/current');
}

export function presentManagedEmbeddingError(
  error: unknown,
): ManagedEmbeddingErrorPresentation {
  if (error instanceof ApiException) {
    return managedEmbeddingErrorPresentation({
      status: error.status,
      error: error.message,
      hint: error.hint,
    });
  }
  return managedEmbeddingErrorPresentation({
    hint: error instanceof Error ? error.message : String(error),
  });
}
