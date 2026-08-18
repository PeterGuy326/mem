/**
 * Domain types — mirror SPEC.md §6 (data model) and §7/§8 (API surface).
 * Backend will eventually generate these from OpenAPI / protobuf; for now we
 * hand-write them and keep them in sync with the spec.
 */

export type IndexStatus = 'pending' | 'processing' | 'done' | 'partial' | 'failed';

export type FileKind = 'image' | 'doc' | 'audio' | 'video' | 'pdf' | 'text' | 'other';

export interface Geo {
  lat: number;
  lon: number;
}

export type FileSourceKind =
  | 'api'
  | 'web'
  | 'cli'
  | 'mcp'
  | 'mobile'
  | 'ai_device'
  | 'import'
  | 'other';

export interface FileSourceLocation extends Geo {
  accuracy_m?: number;
  label?: string;
}

/** Bounded caller/device provenance supplied at upload time. */
export interface FileSourceMetadata {
  captured_at?: string;
  location?: FileSourceLocation;
  source_kind?: FileSourceKind;
  source_name?: string;
}

/**
 * Whitelisted deterministic processor observations. Values remain untrusted
 * display data, so consumers must render them as text rather than HTML.
 */
export type FileProcessorMetadata = Record<string, unknown>;

export type FileAnnotationKind = 'description' | 'tag';
export type FileAnnotationStatus = 'pending' | 'accepted' | 'rejected' | 'superseded';
export type FileAnnotationDecision = Extract<FileAnnotationStatus, 'accepted' | 'rejected'>;

/** Flat provenance fields returned by the canonical file detail contract. */
export interface FileAnnotationProvenance {
  source: string;
  provider: string;
  processor: string;
  analysis_version: string;
}

export interface FileAnnotation extends FileAnnotationProvenance {
  id: string;
  file_id: string;
  stable_key: string;
  kind: FileAnnotationKind;
  value_text: string;
  confidence: number;
  status: FileAnnotationStatus;
  state_version: number;
  decided_by_user_id?: string;
  decided_at?: string;
  created_at: string;
  updated_at: string;
}

/** Coarse type filter exposed in /v1/search (see SPEC §8.1 mem_search). */
// The backend currently treats this value as a MIME prefix. "doc" is not a
// valid prefix for text, PDF, and Office files, so the UI deliberately omits
// that misleading filter until the API grows a document alias.
export type SearchTypeFilter = 'image' | 'audio' | 'any';

export type TokenScope = 'search' | 'read' | 'write' | 'delete' | 'admin';

export interface User {
  id: string;
  email: string;
  created_at: string;
}

export interface Quota {
  calls_per_day?: number;
  storage_bytes?: number;
  ai_tokens_per_day?: number;
}

export interface Token {
  id: string;
  user_id: string;
  name: string;
  scopes: TokenScope[];
  quota: Quota;
  paths: string[];
  expires_at: string | null;
  redact_pii: boolean;
  created_at: string;
}

/** SPEC §6.1 `files` row + a few derived URLs the API will return. */
export interface MemFile {
  id: string;
  user_id: string;
  name: string;
  path: string;
  size: number;
  sha256: string;
  mime: string;
  storage_key: string;

  // AI fields
  summary: string | null;
  caption: string | null;
  tags: string[];
  user_tags: string[];
  timeline_at: string | null;
  geo: Geo | null;
  source_metadata: FileSourceMetadata;
  processor_metadata: FileProcessorMetadata;
  annotations: FileAnnotation[];
  annotations_truncated?: boolean;

  // Status
  index_status: IndexStatus;

  // Timestamps
  created_at: string;
  updated_at: string;

  // Derived (API-only)
  kind: FileKind;
  preview_url: string | null;
  thumbnail_url: string | null;
  download_url: string | null;
  entities?: Entity[];
}

export interface Entity {
  id: string;
  user_id?: string;
  type: 'person' | 'place' | 'org' | 'event';
  name: string;
  metadata?: Record<string, unknown>;
  created_at?: string;
}

/** Single search hit — shape matches SPEC §8.1 mem_search output. */
export interface SearchResult {
  file: MemFile;
  score: number;
  snippet: string | null;
  /** Which retrieval channel produced this hit (for tinting / debugging). */
  channel?: 'visual' | 'text' | 'metadata' | 'fused';
  evidence_id?: string;
  content_sha256?: string;
  chunk_index?: number;
}

export interface SearchResponse {
  results: SearchResult[];
  total: number;
  query_plan?: {
    entities: string[];
    semantic_query: string;
  };
  _meta?: {
    quota_remaining?: number;
    latency_ms?: number;
  };
}

export interface RelatedResponse {
  results: Array<{
    file: MemFile;
    relation: string;
    score: number;
  }>;
}

export interface ListFilesResponse {
  files: MemFile[];
  limit?: number;
  page?: number;
}

export interface AuthLoginResponse {
  token: string;
  user: User;
}

/** Standardized error envelope. SPEC §8.2 says `{ error, hint }`. */
export interface ApiError {
  error: string;
  hint?: string;
  status: number;
}

// ---- Workspace / capabilities ----

export type WorkspaceRole = 'owner' | 'admin' | 'member' | string;

export interface Workspace {
  id: string;
  name: string;
  resource_owner_user_id: string;
  role: WorkspaceRole;
  created_at: string;
}

export interface Capabilities {
  deployment_mode: string;
  registration_mode: string;
  workspace: Workspace;
  features: {
    context: boolean;
    handoff: boolean;
    memory?: boolean;
    ask?: boolean;
    workspace_export: boolean;
    workspace_import: boolean;
  };
  handoff_schema_versions?: number[];
  workspace_restore_modes: WorkspaceRestoreMode[];
  workspace_bundle_schema_versions: number[];
  permissions: {
    read: boolean;
    search: boolean;
    write: boolean;
    delete: boolean;
    provider_read: boolean;
    provider_modify: boolean;
    workspace_export: boolean;
    workspace_import: boolean;
  };
}

// ---- Portable workspace transfer ----

export type WorkspaceRestoreMode = 'fresh';

export interface WorkspaceObjectCounts {
  folders: number;
  files: number;
  memories: number;
  memory_events: number;
  tasks: number;
  checkpoints: number;
  checkpoint_refs: number;
  checkpoint_payloads: number;
  blobs: number;
  blob_bytes: number;
}

export interface WorkspaceImportResult {
  bundle_id: string;
  archive_sha256: string;
  source_workspace_id: string;
  imported_at: string;
  counts: WorkspaceObjectCounts;
  replayed: boolean;
}

export interface WorkspaceImportConflict {
  kind: string;
  resource?: string;
  value?: string;
}

export interface WorkspaceImportConflictResponse {
  error: 'workspace_import_conflict';
  hint: string;
  conflicts: WorkspaceImportConflict[];
  /** Confirmed lower bound when the server caps conflict enumeration. */
  total?: number;
  truncated?: boolean;
}

// ---- Structured Agent memory control plane ----

export const MEMORY_KINDS = [
  'observation',
  'decision',
  'preference',
  'task_state',
  'fact',
  'note',
  'artifact',
] as const;

export type MemoryKind = (typeof MEMORY_KINDS)[number];
export type MemoryLifecycle = 'active' | 'archived';
export type MemoryLifecycleFilter = MemoryLifecycle | 'all';
export type MemoryFeedbackAction = 'useful' | 'not_useful' | 'pin' | 'unpin';
export type MemoryForgetReason = 'user_request' | 'incorrect' | 'sensitive' | 'expired' | 'other';

/**
 * Full public memory row returned by the detail endpoint. Content and
 * provenance values are untrusted source data and must be rendered as plain
 * text / JSON; lifecycle mutations use MemoryControlState instead.
 */
export interface AgentMemoryRecord {
  id: string;
  workspace_id: string;
  created_by_user_id?: string;
  kind: MemoryKind;
  content: string;
  attributes: Record<string, unknown>;
  path: string;
  event_at?: string | null;
  source_type: string;
  source_ref?: string;
  source_file_id?: string;
  source_file_sha256?: string;
  source_locator: Record<string, unknown>;
  producer_agent?: string;
  producer_session?: string;
  producer_task?: string;
  content_sha256: string;
  lifecycle_status: MemoryLifecycle;
  state_version: number;
  pinned: boolean;
  pinned_at?: string | null;
  useful_count: number;
  not_useful_count: number;
  feedback_score: number;
  feedback_count: number;
  feedback_at?: string | null;
  created_at: string;
  updated_at: string;
}

export interface MemoryProvenance {
  workspace_id: string;
  created_by_user_id?: string;
  event_at?: string | null;
  source_type: string;
  source_ref?: string;
  source_file_id?: string;
  source_file_sha256?: string;
  source_locator: Record<string, unknown>;
  producer_agent?: string;
  producer_session?: string;
  producer_task?: string;
}

/** Full `GET /v1/memories/{id}` projection. */
export interface AgentMemory extends AgentMemoryRecord {
  citation: string;
  provenance: MemoryProvenance;
}

/** Bounded list projection; full content and JSON provenance stay detail-only. */
export interface AgentMemorySummary {
  id: string;
  workspace_id: string;
  kind: MemoryKind;
  excerpt: string;
  content_length: number;
  path: string;
  event_at?: string | null;
  source_type: string;
  source_ref?: string;
  source_file_id?: string;
  source_file_sha256?: string;
  producer_agent?: string;
  producer_session?: string;
  producer_task?: string;
  content_sha256: string;
  lifecycle_status: MemoryLifecycle;
  state_version: number;
  /** True when an active memory supersedes or corrects this one. */
  superseded: boolean;
  pinned: boolean;
  pinned_at?: string | null;
  useful_count: number;
  not_useful_count: number;
  feedback_score: number;
  feedback_count: number;
  feedback_at?: string | null;
  citation: string;
  created_at: string;
  updated_at: string;
}

export interface ListMemoriesResponse {
  memories: AgentMemorySummary[];
  next_cursor?: string | null;
}

export interface MemoryEvent {
  id: string;
  /** Older detail/audit projections may include it; control responses omit it. */
  workspace_id?: string;
  memory_id: string;
  action: MemoryFeedbackAction | 'archive' | 'restore' | 'forget';
  actor_user_id?: string;
  expected_version: number;
  resulting_version: number;
  reason?: MemoryForgetReason;
  created_at: string;
}

/**
 * Bounded control projection. Mutations never echo memory content, paths, or
 * provenance into an Agent/MCP context.
 */
export interface MemoryControlState {
  id: string;
  lifecycle_status: MemoryLifecycle;
  state_version: number;
  pinned: boolean;
  pinned_at?: string | null;
  useful_count: number;
  not_useful_count: number;
  feedback_score: number;
  feedback_count: number;
  feedback_at?: string | null;
  updated_at: string;
}

/** `feedback`, `archive`, and `restore` return only control state. */
export interface MemoryMutationResponse {
  memory: MemoryControlState;
  event: MemoryEvent;
  replayed: boolean;
}

/** `forget` deliberately returns only the retry-safe public tombstone fields. */
export interface MemoryForgetResponse {
  memory_id: string;
  state_version: number;
  forgotten_at?: string;
  event: MemoryEvent;
  replayed: boolean;
}

/** Immutable memory-to-memory edge types. */
export type MemoryRelationType = 'supersedes' | 'corrects' | 'occurrence_of';

/** One immutable relation edge from `GET/POST /v1/memory-relations`. */
export interface MemoryRelation {
  id: string;
  workspace_id: string;
  source_id: string;
  target_id: string;
  relation_type: MemoryRelationType;
  actor_user_id?: string;
  reason?: string;
  created_at: string;
}

export interface ListMemoryRelationsResponse {
  relations: MemoryRelation[];
}

export interface CreateMemoryRelationRequest {
  source_id: string;
  target_id: string;
  relation_type: MemoryRelationType;
  reason?: string;
}

// ---- Portable Agent handoff v1 ----

export type TaskStatus = 'in_progress' | 'ready' | 'blocked' | 'complete';
export type CheckpointKind = 'checkpoint' | 'handoff';

export interface HandoffProgress {
  summary: string;
  completed: string[];
}

export interface HandoffDecision {
  summary: string;
  rationale?: string;
  references: string[];
}

export interface HandoffNextStep {
  summary: string;
  references: string[];
}

export interface HandoffBlocker {
  summary: string;
  needs?: string;
  references: string[];
}

export interface HandoffArtifact {
  uri: string;
  role?: string;
  sha256?: string;
  required: boolean;
}

export interface HandoffVCSState {
  revision?: string;
  branch?: string;
  dirty?: boolean;
  status_summary?: string;
}

export interface HandoffWorkspaceState {
  working_directory?: string;
  vcs?: HandoffVCSState;
}

export interface HandoffState {
  status: TaskStatus;
  goal: string;
  progress: HandoffProgress;
  decisions: HandoffDecision[];
  next_steps: HandoffNextStep[];
  blockers: HandoffBlocker[];
  open_questions: string[];
  artifacts: HandoffArtifact[];
  workspace_state?: HandoffWorkspaceState;
}

export interface HandoffProducer {
  agent_id: string;
  session_id?: string;
}

export interface HandoffV1 {
  contract: 'mem.handoff';
  schema_version: 1;
  checkpoint_kind: CheckpointKind;
  task_key: string;
  base_checkpoint_id?: string | null;
  scope_path: string;
  state: HandoffState;
  producer: HandoffProducer;
}

export interface AgentTask {
  id: string;
  workspace_id: string;
  task_key: string;
  scope_path: string;
  head_checkpoint_id?: string | null;
  head_sequence: number;
  created_at: string;
  updated_at: string;
}

export interface HandoffReference {
  checkpoint_id: string;
  ordinal: number;
  relation: string;
  uri: string;
  expected_sha256?: string;
  required: boolean;
  metadata: unknown;
}

export interface CheckpointSummary {
  id: string;
  workspace_id: string;
  task_id: string;
  task_key: string;
  sequence: number;
  checkpoint_kind: CheckpointKind;
  contract: string;
  schema_version: number;
  base_checkpoint_id?: string | null;
  scope_path: string;
  status: TaskStatus;
  progress_excerpt: string;
  progress_length: number;
  completed_count: number;
  reference_count: number;
  payload_sha256: string;
  producer_agent: string;
  producer_session?: string;
  created_at: string;
}

export interface CheckpointRecord {
  id: string;
  workspace_id: string;
  task_id: string;
  task_key: string;
  sequence: number;
  checkpoint_kind: CheckpointKind;
  contract: string;
  schema_version: number;
  base_checkpoint_id?: string | null;
  scope_path: string;
  handoff: HandoffV1;
  payload_sha256: string;
  created_by_user_id?: string;
  producer_agent: string;
  producer_session?: string;
  created_at: string;
  references: HandoffReference[];
}

export type ReferenceResolutionStatus =
  | 'available'
  | 'unavailable'
  | 'hash_mismatch'
  | 'external_unverified'
  | string;

export interface ResolvedHandoffReference {
  uri: string;
  relation: string;
  required: boolean;
  status: ReferenceResolutionStatus;
  citation?: string;
  actual_sha256?: string;
}

export interface ContextLocator {
  kind: 'text_chunk' | 'visual_caption' | 'memory_text' | string;
  chunk_index?: number;
}

export interface ContextProvenance {
  source_type: string;
  source_ref?: string;
  source_file_id?: string;
  source_file_sha256?: string;
  source_locator?: unknown;
  agent_id?: string;
  session_id?: string;
  task_id?: string;
}

export interface ContextEvidence {
  evidence_id: string;
  source_kind: 'file' | 'memory' | string;
  source_id: string;
  citation: string;
  file_id?: string;
  memory_id?: string;
  memory_kind?: string;
  name?: string;
  path: string;
  mime?: string;
  content_sha256: string;
  content_url: string;
  locator: ContextLocator;
  excerpt: string;
  score: number;
  route: string;
  reason?: string;
  provenance?: ContextProvenance;
  timeline_at?: string;
}

export interface ContextPackWarning {
  source: string;
  code: string;
  message: string;
}

export interface ContextPack {
  query: string;
  scope: string;
  source: string;
  evidence: ContextEvidence[];
  total_chars: number;
  partial: boolean;
  warnings?: ContextPackWarning[];
  retrieved_at: string;
}

export interface ResumeWarning {
  code: string;
  message: string;
}

export interface ResumeRequest {
  checkpoint_id?: string;
  scope?: string;
  focus?: string;
  limit?: number;
  max_chars?: number;
}

export interface ResumeResponse {
  contract: 'mem.resume' | string;
  schema_version: number;
  task: AgentTask;
  checkpoint: CheckpointRecord;
  resolved: ResolvedHandoffReference[];
  missing: ResolvedHandoffReference[];
  complete: boolean;
  context?: ContextPack;
  warnings?: ResumeWarning[];
  retrieved_at: string;
}
