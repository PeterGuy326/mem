import * as React from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Search, Filter, Image as ImageIcon, FileText, Music, FileQuestion, Sparkles, ArrowLeft } from 'lucide-react';
import { Input } from '@/components/ui/Input';
import { Button } from '@/components/ui/Button';
import { Badge } from '@/components/ui/Badge';
import { Skeleton } from '@/components/ui/Skeleton';
import { EmptyState } from '@/components/ui/EmptyState';
import { Kbd } from '@/components/ui/Kbd';
import { cn } from '@/lib/cn';
import { useSearch } from '@/hooks/useFiles';
import { formatDate } from '@/lib/format';
import type { FileKind, SearchResult, SearchTypeFilter } from '@/lib/types';

const TYPE_OPTIONS: { value: SearchTypeFilter; label: string }[] = [
  { value: 'any', label: '全部' },
  { value: 'image', label: '图片' },
  { value: 'doc', label: '文档' },
  { value: 'audio', label: '音频' },
];

const SAMPLE_QUERIES = [
  '草地上的金毛',
  '2012 年和小明在云南拍的照片',
  '去年关于 RAG 的笔记',
  '租房合同',
  '妈妈的合影',
];

export function SearchPage() {
  const [params, setParams] = useSearchParams();
  const initialQ = params.get('q') ?? '';
  const [q, setQ] = React.useState(initialQ);
  const [type, setType] = React.useState<SearchTypeFilter>(
    (params.get('type') as SearchTypeFilter) ?? 'any',
  );
  const [since, setSince] = React.useState<string>(params.get('since') ?? '');
  const [until, setUntil] = React.useState<string>(params.get('until') ?? '');

  // Debounce the actual query firing.
  const [debouncedQ, setDebouncedQ] = React.useState(initialQ);
  React.useEffect(() => {
    const t = setTimeout(() => setDebouncedQ(q), 250);
    return () => clearTimeout(t);
  }, [q]);

  // Sync URL when query / filters change.
  React.useEffect(() => {
    const next = new URLSearchParams();
    if (debouncedQ) next.set('q', debouncedQ);
    if (type && type !== 'any') next.set('type', type);
    if (since) next.set('since', since);
    if (until) next.set('until', until);
    setParams(next, { replace: true });
  }, [debouncedQ, type, since, until, setParams]);

  const { data, isFetching } = useSearch(
    {
      q: debouncedQ,
      type: type === 'any' ? undefined : type,
      since: since || undefined,
      until: until || undefined,
    },
    { enabled: debouncedQ.length > 0 },
  );

  const hasQuery = debouncedQ.trim().length > 0;
  const results = data?.results ?? [];

  return (
    <div className="mx-auto max-w-6xl px-8 py-10">
      {/* Hero search */}
      <section className="mb-6">
        <Link
          to="/drive"
          className="inline-flex items-center gap-1.5 text-sm text-fg-muted hover:text-fg transition-colors mb-3"
        >
          <ArrowLeft className="h-3.5 w-3.5" /> 返回云盘
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">搜索</h1>
        <p className="mt-1.5 text-sm text-fg-muted">用自然语言找回任何东西。</p>
        <div className="mt-5 relative">
          <Input
            value={q}
            autoFocus
            onChange={(e) => setQ(e.target.value)}
            placeholder="搜索图片、文档、人物… 比如 “2012 年和小明在云南拍的照片”"
            leadingIcon={<Search />}
            trailing={isFetching ? <span className="text-2xs text-fg-subtle">…</span> : <Kbd>⏎</Kbd>}
            className="h-12 text-base"
          />
        </div>
        {!hasQuery && (
          <div className="mt-4 flex flex-wrap items-center gap-2">
            <span className="text-xs text-fg-subtle mr-1">试试搜:</span>
            {SAMPLE_QUERIES.map((s) => (
              <button
                key={s}
                onClick={() => setQ(s)}
                className="rounded-full border border-border bg-bg-subtle hover:bg-bg-inset hover:border-border-strong
                           px-3 py-1 text-xs text-fg-muted hover:text-fg transition-colors"
              >
                {s}
              </button>
            ))}
          </div>
        )}
      </section>

      {/* Filters */}
      <section className="mb-6 flex flex-wrap items-center gap-3 text-sm">
        <div className="flex items-center gap-1.5 text-fg-muted">
          <Filter className="h-3.5 w-3.5" />
          <span className="text-xs uppercase tracking-wider">过滤</span>
        </div>
        <div className="flex items-center gap-1 rounded-md border border-border bg-bg-subtle p-0.5">
          {TYPE_OPTIONS.map((opt) => (
            <button
              key={opt.value}
              onClick={() => setType(opt.value)}
              className={cn(
                'px-3 h-7 text-xs rounded transition-colors',
                type === opt.value
                  ? 'bg-bg-panel text-fg shadow-soft'
                  : 'text-fg-muted hover:text-fg',
              )}
            >
              {opt.label}
            </button>
          ))}
        </div>
        <DateField label="自" value={since} onChange={setSince} />
        <DateField label="至" value={until} onChange={setUntil} />
        {(since || until || type !== 'any') && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setType('any');
              setSince('');
              setUntil('');
            }}
          >
            清除
          </Button>
        )}
        <div className="ml-auto text-2xs text-fg-subtle inline-flex items-center gap-2">
          <span className="inline-flex items-center gap-1 rounded-md border border-dashed border-border px-2 py-0.5">
            <Sparkles className="h-3 w-3" /> 人脸过滤 W2 上线
          </span>
        </div>
      </section>

      {/* Query plan / meta */}
      {hasQuery && data?.query_plan?.entities && data.query_plan.entities.length > 0 && (
        <div className="mb-4 text-xs text-fg-muted flex items-center gap-2">
          <span>检测到实体:</span>
          {data.query_plan.entities.map((e) => (
            <Badge key={e} tone="accent">
              {e}
            </Badge>
          ))}
        </div>
      )}

      {/* Results */}
      {!hasQuery ? (
        <EmptyState
          icon={<Search />}
          title="开始搜索"
          description="语义 + 视觉 + 元数据多路召回。支持中文、按时间过滤、按人物过滤（W2）。"
        />
      ) : isFetching && !data ? (
        <ResultGridSkeleton />
      ) : results.length === 0 ? (
        <EmptyState
          icon={<Search />}
          title="没有命中"
          description="试试换个说法，比如把日期范围去掉，或者换成英文关键词。"
        />
      ) : (
        <ResultGrid results={results} />
      )}

      {/* Footer meta */}
      {hasQuery && data && (
        <div className="mt-10 text-2xs text-fg-subtle text-center">
          共 {data.total} 条结果 · 用时 {data._meta?.latency_ms ?? '?'} ms · 多路融合（visual / text / metadata）
        </div>
      )}
    </div>
  );
}

function DateField({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <label className="inline-flex items-center gap-2 text-xs text-fg-muted">
      <span>{label}</span>
      <input
        type="date"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="h-7 rounded-md border border-border bg-bg-inset px-2 text-xs text-fg outline-none focus:border-accent/60"
      />
    </label>
  );
}

function ResultGrid({ results }: { results: SearchResult[] }) {
  // Split images vs docs/audio. Images go to a masonry-ish CSS columns layout;
  // docs/audio go to a list. Together they read like Linear's mixed views.
  const images = results.filter((r) => r.file.kind === 'image');
  const others = results.filter((r) => r.file.kind !== 'image');

  return (
    <div className="grid grid-cols-1 lg:grid-cols-[1fr_360px] gap-8 items-start">
      <section>
        <div className="mb-3 flex items-center justify-between">
          <h3 className="text-xs uppercase tracking-wider text-fg-muted font-medium">
            视觉结果 · {images.length}
          </h3>
        </div>
        {images.length === 0 ? (
          <div className="text-xs text-fg-subtle py-6">这次搜索没有图片命中</div>
        ) : (
          <div className="columns-2 md:columns-3 gap-3 [column-fill:_balance]">
            {images.map((r) => (
              <ImageResultCard key={r.file.id} result={r} />
            ))}
          </div>
        )}
      </section>
      <aside className="lg:sticky lg:top-20">
        <h3 className="mb-3 text-xs uppercase tracking-wider text-fg-muted font-medium">
          文档与音频 · {others.length}
        </h3>
        {others.length === 0 ? (
          <div className="text-xs text-fg-subtle py-6">无</div>
        ) : (
          <ol className="surface divide-y divide-border">
            {others.map((r) => (
              <DocResultRow key={r.file.id} result={r} />
            ))}
          </ol>
        )}
      </aside>
    </div>
  );
}

function kindIcon(kind: FileKind) {
  if (kind === 'image') return ImageIcon;
  if (kind === 'audio') return Music;
  if (kind === 'doc' || kind === 'pdf' || kind === 'text') return FileText;
  return FileQuestion;
}

function ScoreBadge({ score }: { score: number }) {
  // Convert raw score (~0-3) to a 0-100% feel for display.
  const pct = Math.min(99, Math.max(1, Math.round(score * 30)));
  return (
    <Badge tone="muted" className="font-mono">
      {pct}
    </Badge>
  );
}

function ImageResultCard({ result }: { result: SearchResult }) {
  const f = result.file;
  return (
    <Link
      to={`/files/${f.id}`}
      className="group block mb-3 break-inside-avoid rounded-lg overflow-hidden border border-border
                 bg-bg-panel hover:border-border-strong hover:shadow-soft transition-all"
    >
      <div className="relative bg-bg-inset">
        {f.thumbnail_url ? (
          <img
            src={f.thumbnail_url}
            alt={f.caption ?? f.name}
            loading="lazy"
            className="w-full h-auto block transition-transform duration-500 group-hover:scale-[1.02]"
          />
        ) : (
          <div className="aspect-[4/3] grid place-items-center text-fg-subtle">
            <ImageIcon className="h-8 w-8" />
          </div>
        )}
        <div
          className="absolute inset-0 opacity-0 group-hover:opacity-100 transition-opacity
                     bg-gradient-to-t from-bg/95 via-bg/40 to-transparent flex items-end p-3"
        >
          <div className="text-xs text-fg leading-snug line-clamp-3">
            {f.caption ?? f.name}
          </div>
        </div>
      </div>
      <div className="px-3 py-2 flex items-center gap-2">
        <div className="text-2xs text-fg-subtle flex-1 truncate">
          {f.timeline_at ? formatDate(f.timeline_at) : '—'}
        </div>
        <ScoreBadge score={result.score} />
      </div>
    </Link>
  );
}

function DocResultRow({ result }: { result: SearchResult }) {
  const f = result.file;
  const Icon = kindIcon(f.kind);
  return (
    <Link
      to={`/files/${f.id}`}
      className="group flex items-start gap-3 px-4 py-3 hover:bg-bg-inset/60 transition-colors"
    >
      <div className="h-9 w-9 flex-none rounded-md bg-bg-inset border border-border grid place-items-center text-fg-muted">
        <Icon className="h-4 w-4" />
      </div>
      <div className="flex-1 min-w-0">
        <div className="text-sm text-fg truncate group-hover:text-accent transition-colors">
          {f.name}
        </div>
        {result.snippet && (
          <div className="text-2xs text-fg-muted mt-1 leading-relaxed line-clamp-2">
            {result.snippet}
          </div>
        )}
        <div className="text-2xs text-fg-subtle mt-1.5">
          {formatDate(f.timeline_at ?? f.created_at)}
        </div>
      </div>
      <ScoreBadge score={result.score} />
    </Link>
  );
}

function ResultGridSkeleton() {
  return (
    <div className="grid grid-cols-1 lg:grid-cols-[1fr_360px] gap-8 items-start">
      <div className="columns-2 md:columns-3 gap-3">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="mb-3 h-48 w-full" />
        ))}
      </div>
      <div className="space-y-3">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-14 w-full" />
        ))}
      </div>
    </div>
  );
}

