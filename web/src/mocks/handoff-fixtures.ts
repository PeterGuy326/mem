import type {
  AgentTask,
  Capabilities,
  CheckpointKind,
  CheckpointRecord,
  ContextPack,
  HandoffReference,
  HandoffState,
  ResumeResponse,
  TaskStatus,
  Workspace,
} from '@/lib/types';

export const MOCK_WORKSPACE: Workspace = {
  id: '11111111-1111-4111-8111-111111111111',
  name: 'Agent Drive Demo',
  resource_owner_user_id: 'user-1',
  role: 'owner',
  created_at: '2026-07-01T08:00:00Z',
};

export const MOCK_CAPABILITIES: Capabilities = {
  deployment_mode: 'personal',
  registration_mode: 'open',
  workspace: MOCK_WORKSPACE,
  features: {
    context: true,
    handoff: true,
    memory: true,
    ask: false,
    workspace_export: true,
    workspace_import: true,
  },
  handoff_schema_versions: [1],
  workspace_restore_modes: ['fresh'],
  workspace_bundle_schema_versions: [1, 2],
  permissions: {
    read: true,
    search: true,
    write: true,
    delete: true,
    provider_read: true,
    provider_modify: true,
    workspace_export: true,
    workspace_import: true,
  },
};

const TASK_1 = 'claude-to-codex-demo';
const TASK_2 = 'visual-search-regression';
const CP_1 = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1';
const CP_2 = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2';
const CP_3 = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb1';
const HERO_URI = 'mem://files/hero-2012-yunnan-xiaoming';
const MISSING_URI = 'mem://files/00000000-0000-4000-8000-00000000dead';

function state(
  status: TaskStatus,
  progressSummary: string,
  overrides: Partial<HandoffState> = {},
): HandoffState {
  return {
    status,
    goal: '让 Claude Code 写入的标准任务状态可以由 Codex 无痛恢复，并保留全部出处。',
    progress: {
      summary: progressSummary,
      completed: ['定义 handoff.v1 契约', '实现 checkpoint 与 resume API'],
    },
    decisions: [
      {
        summary: '使用不可变线性检查点，而不是覆盖当前状态。',
        rationale: '这样可以保留来源、历史版本和并发冲突证据。',
        references: [HERO_URI],
      },
    ],
    next_steps: [
      {
        summary: '从 Web 任务账本验证恢复结果和缺失引用。',
        references: [HERO_URI],
      },
    ],
    blockers: [],
    open_questions: ['跨设备导出包的冲突策略需要单独定义。'],
    artifacts: [
      {
        uri: HERO_URI,
        role: 'acceptance-evidence',
        sha256: 'a'.repeat(64),
        required: true,
      },
      {
        uri: 'https://example.com/handoff-notes',
        role: 'external-note',
        required: false,
      },
    ],
    workspace_state: {
      working_directory: '/workspace/mem',
      vcs: {
        branch: 'codex/web-task-ledger',
        revision: '4f2c8b1',
        dirty: true,
        status_summary: 'Web task ledger files are modified but not committed.',
      },
    },
    ...overrides,
  };
}

function references(
  checkpointID: string,
  artifactURI = HERO_URI,
  required = true,
): HandoffReference[] {
  return [
    {
      checkpoint_id: checkpointID,
      ordinal: 0,
      relation: 'artifact',
      uri: artifactURI,
      expected_sha256: required ? 'a'.repeat(64) : undefined,
      required,
      metadata: {},
    },
  ];
}

function checkpoint(input: {
  id: string;
  taskID: string;
  taskKey: string;
  sequence: number;
  kind: CheckpointKind;
  status: TaskStatus;
  summary: string;
  createdAt: string;
  agent: string;
  session?: string;
  base?: string;
  stateOverrides?: Partial<HandoffState>;
  refs?: HandoffReference[];
}): CheckpointRecord {
  const handoffState = state(input.status, input.summary, input.stateOverrides);
  return {
    id: input.id,
    workspace_id: MOCK_WORKSPACE.id,
    task_id: input.taskID,
    task_key: input.taskKey,
    sequence: input.sequence,
    checkpoint_kind: input.kind,
    contract: 'mem.handoff',
    schema_version: 1,
    base_checkpoint_id: input.base,
    scope_path: input.taskKey === TASK_1 ? '/Projects/mem' : '/Photos',
    handoff: {
      contract: 'mem.handoff',
      schema_version: 1,
      checkpoint_kind: input.kind,
      task_key: input.taskKey,
      base_checkpoint_id: input.base,
      scope_path: input.taskKey === TASK_1 ? '/Projects/mem' : '/Photos',
      state: handoffState,
      producer: {
        agent_id: input.agent,
        session_id: input.session,
      },
    },
    payload_sha256: input.sequence === 1 ? '1'.repeat(64) : '2'.repeat(64),
    created_by_user_id: 'user-1',
    producer_agent: input.agent,
    producer_session: input.session,
    created_at: input.createdAt,
    references: input.refs ?? references(input.id),
  };
}

const checkpointOne = checkpoint({
  id: CP_1,
  taskID: '22222222-2222-4222-8222-222222222221',
  taskKey: TASK_1,
  sequence: 1,
  kind: 'checkpoint',
  status: 'in_progress',
  summary: '后端契约已定义，仍需接入 Web 可视化。',
  createdAt: '2026-07-27T08:30:00Z',
  agent: 'claude-code',
  session: 'claude-session-42',
});

const checkpointTwo = checkpoint({
  id: CP_2,
  taskID: '22222222-2222-4222-8222-222222222221',
  taskKey: TASK_1,
  sequence: 2,
  kind: 'handoff',
  status: 'ready',
  summary: 'API、CLI 与 MCP 已对齐，任务可交给 Codex 继续。',
  createdAt: '2026-07-28T03:12:00Z',
  agent: 'claude-code',
  session: 'claude-session-42',
  base: CP_1,
});

const checkpointThree = checkpoint({
  id: CP_3,
  taskID: '22222222-2222-4222-8222-222222222222',
  taskKey: TASK_2,
  sequence: 1,
  kind: 'handoff',
  status: 'blocked',
  summary: '真实视觉检索回归被缺失的原图对象阻塞。',
  createdAt: '2026-07-28T04:05:00Z',
  agent: 'codex',
  session: 'codex-session-9',
  stateOverrides: {
    goal: '验证“2012 年和小明在云南拍的照片”可通过自然语言搜图找回原件。',
    blockers: [
      {
        summary: '对象存储缺少验收原图。',
        needs: '重新上传带稳定哈希的 seed 图片。',
        references: [MISSING_URI],
      },
    ],
    artifacts: [
      {
        uri: MISSING_URI,
        role: 'required-regression-image',
        sha256: 'a'.repeat(64),
        required: true,
      },
    ],
  },
  refs: references(CP_3, MISSING_URI, true),
});

export const MOCK_TASKS: AgentTask[] = [
  {
    id: checkpointOne.task_id,
    workspace_id: MOCK_WORKSPACE.id,
    task_key: TASK_1,
    scope_path: '/Projects/mem',
    head_checkpoint_id: CP_2,
    head_sequence: 2,
    created_at: checkpointOne.created_at,
    updated_at: checkpointTwo.created_at,
  },
  {
    id: checkpointThree.task_id,
    workspace_id: MOCK_WORKSPACE.id,
    task_key: TASK_2,
    scope_path: '/Photos',
    head_checkpoint_id: CP_3,
    head_sequence: 1,
    created_at: checkpointThree.created_at,
    updated_at: checkpointThree.created_at,
  },
];

export const MOCK_CHECKPOINTS_BY_TASK: Record<string, CheckpointRecord[]> = {
  [TASK_1]: [checkpointTwo, checkpointOne],
  [TASK_2]: [checkpointThree],
};

function contextPack(): ContextPack {
  return {
    query: '让 Claude Code 写入的标准任务状态可以由 Codex 无痛恢复',
    scope: '/Projects/mem',
    source: 'all',
    evidence: [
      {
        evidence_id: 'visual:hero-2012-yunnan-xiaoming',
        source_kind: 'file',
        source_id: 'hero-2012-yunnan-xiaoming',
        citation: HERO_URI,
        file_id: 'hero-2012-yunnan-xiaoming',
        name: 'IMG_2012_DALI_0427.jpg',
        path: '/Photos/2012/IMG_2012_DALI_0427.jpg',
        mime: 'image/jpeg',
        content_sha256: 'a'.repeat(64),
        content_url: '/v1/files/hero-2012-yunnan-xiaoming/content',
        locator: { kind: 'visual_caption' },
        excerpt: '2012 年夏天，云南大理洱海边，和小明的合影。',
        score: 0.91,
        route: 'visual',
        reason: 'semantic_visual',
        timeline_at: '2012-07-14T15:22:00Z',
      },
    ],
    total_chars: 26,
    partial: false,
    retrieved_at: '2026-07-28T04:10:00Z',
  };
}

export function mockResume(taskKey: string, checkpointID?: string): ResumeResponse | null {
  const task = MOCK_TASKS.find((candidate) => candidate.task_key === taskKey);
  const checkpoints = MOCK_CHECKPOINTS_BY_TASK[taskKey] ?? [];
  const selected = checkpointID
    ? checkpoints.find((candidate) => candidate.id === checkpointID)
    : checkpoints[0];
  if (!task || !selected) return null;

  const missingRequired = selected.handoff.state.artifacts.some(
    (artifact) => artifact.required && artifact.uri === MISSING_URI,
  );
  return {
    contract: 'mem.resume',
    schema_version: 1,
    task,
    checkpoint: selected,
    resolved: missingRequired
      ? []
      : [
          {
            uri: HERO_URI,
            relation: 'artifact',
            required: true,
            status: 'available',
            citation: HERO_URI,
            actual_sha256: 'a'.repeat(64),
          },
        ],
    missing: missingRequired
      ? [
          {
            uri: MISSING_URI,
            relation: 'artifact',
            required: true,
            status: 'unavailable',
          },
        ]
      : [
          {
            uri: 'https://example.com/handoff-notes',
            relation: 'artifact',
            required: false,
            status: 'external_unverified',
          },
        ],
    complete: !missingRequired,
    context: missingRequired ? undefined : contextPack(),
    warnings: missingRequired
      ? [
          {
            code: 'context_unavailable',
            message:
              'Required file evidence is unavailable; deterministic state was still restored.',
          },
        ]
      : [],
    retrieved_at: '2026-07-28T04:10:00Z',
  };
}
