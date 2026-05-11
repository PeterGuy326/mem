import { Card, CardBody, CardHeader, CardTitle } from '@/components/ui/Card';
import { Badge } from '@/components/ui/Badge';

export function SettingsPage() {
  return (
    <div className="mx-auto max-w-3xl px-8 py-10">
      <h1 className="text-2xl font-semibold tracking-tight">设置</h1>
      <p className="mt-1.5 text-sm text-fg-muted">
        Phase 2 完整版正在路上 — Provider 切换、Token 管理、配额、主题切换都会在这里。
      </p>

      <div className="mt-8 grid gap-4">
        <Card>
          <CardHeader>
            <CardTitle>Provider</CardTitle>
          </CardHeader>
          <CardBody className="text-sm text-fg-muted space-y-2">
            <div className="flex justify-between">
              <span>Embedding (text)</span>
              <Badge tone="neutral">ollama:nomic-embed-text</Badge>
            </div>
            <div className="flex justify-between">
              <span>Embedding (visual)</span>
              <Badge tone="neutral">built-in CLIP ViT-B/32</Badge>
            </div>
            <div className="flex justify-between">
              <span>VLM</span>
              <Badge tone="neutral">ollama:minicpm-v</Badge>
            </div>
            <div className="flex justify-between">
              <span>LLM</span>
              <Badge tone="neutral">anthropic:claude-opus-4-7</Badge>
            </div>
          </CardBody>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Token</CardTitle>
          </CardHeader>
          <CardBody className="text-sm text-fg-muted">
            <div>当前 token 仅用于本机演示，Phase 2 会接入 token CRUD（scope / quota / paths）。</div>
          </CardBody>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>主题</CardTitle>
          </CardHeader>
          <CardBody className="text-sm text-fg-muted">
            W1 固定深色模式。CSS 变量已就位，W2 提供切换。
          </CardBody>
        </Card>
      </div>
    </div>
  );
}
