import type { AgentMemory } from '@/lib/types';

const WORKSPACE_ID = '11111111-1111-4111-8111-111111111111';
const USER_ID = '22222222-2222-4222-8222-222222222222';

function memory(
  input: Omit<
    AgentMemory,
    | 'workspace_id'
    | 'created_by_user_id'
    | 'attributes'
    | 'source_locator'
    | 'content_sha256'
    | 'state_version'
    | 'pinned'
    | 'useful_count'
    | 'not_useful_count'
    | 'feedback_score'
    | 'feedback_count'
    | 'citation'
    | 'provenance'
    | 'updated_at'
  > &
    Partial<
      Pick<
        AgentMemory,
        | 'attributes'
        | 'source_locator'
        | 'state_version'
        | 'pinned'
        | 'pinned_at'
        | 'useful_count'
        | 'not_useful_count'
        | 'feedback_score'
        | 'feedback_count'
        | 'feedback_at'
      >
    >,
): AgentMemory {
  const record = {
    workspace_id: WORKSPACE_ID,
    created_by_user_id: USER_ID,
    attributes: input.attributes ?? {},
    source_locator: input.source_locator ?? {},
    content_sha256: input.id.replaceAll('-', '').padEnd(64, '0').slice(0, 64),
    state_version: input.state_version ?? 1,
    pinned: input.pinned ?? false,
    pinned_at: input.pinned_at,
    useful_count: input.useful_count ?? 0,
    not_useful_count: input.not_useful_count ?? 0,
    feedback_score: input.feedback_score ?? 0,
    feedback_count: input.feedback_count ?? 0,
    feedback_at: input.feedback_at,
    updated_at: input.created_at,
    ...input,
  };
  return {
    ...record,
    citation: `mem://memories/${input.id}`,
    provenance: {
      workspace_id: WORKSPACE_ID,
      created_by_user_id: USER_ID,
      event_at: record.event_at,
      source_type: record.source_type,
      source_ref: record.source_ref,
      source_file_id: record.source_file_id,
      source_file_sha256: record.source_file_sha256,
      source_locator: record.source_locator,
      producer_agent: record.producer_agent,
      producer_session: record.producer_session,
      producer_task: record.producer_task,
    },
  };
}

export const MEMORY_FIXTURES: AgentMemory[] = [
  memory({
    id: '30000000-0000-4000-8000-000000000001',
    kind: 'decision',
    content: 'mem 只负责返回带来源的 Context Pack；最终推理与回答始终由外部 Agent 完成。',
    path: '/Projects/mem',
    event_at: '2026-07-28T08:30:00Z',
    source_type: 'agent',
    source_ref: 'codex://thread/mem-direction',
    producer_agent: 'codex',
    producer_session: 'session-20260728',
    producer_task: 'memory-plane',
    lifecycle_status: 'active',
    pinned: true,
    pinned_at: '2026-07-28T09:00:00Z',
    useful_count: 3,
    not_useful_count: 0,
    feedback_score: 3,
    feedback_count: 3,
    feedback_at: '2026-07-28T09:20:00Z',
    created_at: '2026-07-28T08:30:00Z',
    attributes: { decision_scope: 'product-boundary', confirmed: true },
  }),
  memory({
    id: '30000000-0000-4000-8000-000000000002',
    kind: 'preference',
    content: '用户偏好：交付说明先给结论，再列验证证据；避免把内部工具名当作产品价值。',
    path: '/Preferences',
    event_at: '2026-07-27T12:10:00Z',
    source_type: 'user_statement',
    producer_agent: 'claude-code',
    producer_session: 'session-style',
    lifecycle_status: 'active',
    useful_count: 2,
    feedback_score: 2,
    feedback_count: 2,
    created_at: '2026-07-27T12:10:00Z',
  }),
  memory({
    id: '30000000-0000-4000-8000-000000000003',
    kind: 'artifact',
    content: '2012 年云南大理旅行照片是多媒体记忆检索的验收锚点。',
    path: '/Photos/2012',
    event_at: '2012-07-14T15:22:00Z',
    source_type: 'file',
    source_ref: 'mem://files/hero-2012-yunnan-xiaoming',
    source_file_id: 'hero-2012-yunnan-xiaoming',
    source_file_sha256: 'a'.repeat(64),
    source_locator: { kind: 'visual_caption' },
    producer_agent: 'mem-indexer',
    lifecycle_status: 'active',
    created_at: '2026-07-26T10:00:00Z',
  }),
  memory({
    id: '30000000-0000-4000-8000-000000000004',
    kind: 'observation',
    content:
      '<script>alert("never execute memory content")</script>\n这段内容用于验证记忆详情只按纯文本渲染。',
    path: '/Security',
    source_type: 'test_fixture',
    producer_agent: 'security-eval',
    lifecycle_status: 'active',
    not_useful_count: 1,
    feedback_score: -1,
    feedback_count: 1,
    created_at: '2026-07-25T08:00:00Z',
    attributes: { rendered_as: 'plain_text', trusted_html: false },
  }),
  memory({
    id: '30000000-0000-4000-8000-000000000005',
    kind: 'task_state',
    content: '旧任务状态：等待接入回答模型。该方向已经被新的产品边界取代。',
    path: '/Projects/mem',
    event_at: '2026-07-20T06:00:00Z',
    source_type: 'agent',
    producer_agent: 'legacy-agent',
    producer_task: 'old-roadmap',
    lifecycle_status: 'archived',
    created_at: '2026-07-20T06:00:00Z',
  }),
  memory({
    id: '30000000-0000-4000-8000-000000000006',
    kind: 'note',
    content:
      '长内容边界验证：' +
      '记忆列表只返回 Unicode 安全的有界摘要，完整内容只能在详情接口读取。'.repeat(28),
    path: '/Projects/mem/Evaluation',
    source_type: 'test_fixture',
    producer_agent: 'web-e2e',
    lifecycle_status: 'active',
    created_at: '2026-07-19T06:00:00Z',
  }),
];

export const MOCK_WORKSPACE_ID = WORKSPACE_ID;
