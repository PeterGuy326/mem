import * as React from 'react';
import { Link, useSearchParams } from 'react-router';
import {
  AlertTriangle,
  ArrowLeft,
  FileQuestion,
  FileText,
  Filter,
  History,
  Image as ImageIcon,
  Music,
  Search,
  X,
} from 'lucide-react';
import { Input } from '@/components/ui/Input';
import { Button } from '@/components/ui/Button';
import { Badge } from '@/components/ui/Badge';
import { Skeleton } from '@/components/ui/Skeleton';
import { EmptyState } from '@/components/ui/EmptyState';
import { Kbd } from '@/components/ui/Kbd';
import { cn } from '@/lib/cn';
import { AuthedImage } from '@/components/ui/AuthedImage';
import { useSearch } from '@/hooks/useFiles';
import { useHistory } from '@/hooks/useHistory';
import { listFaces, getFaceFiles, nameFace, type FaceCluster, type FaceFile } from '@/lib/ai';
import { useT } from '@/i18n';
import { formatDate } from '@/lib/format';
import type { FileKind, SearchResult, SearchTypeFilter } from '@/lib/types';
import { ApiException } from '@/lib/api';

const TYPE_OPTIONS: { value: SearchTypeFilter; labelKey: string }[] = [
  { value: 'any', labelKey: 'search.typeAny' },
  { value: 'image', labelKey: 'search.typeImage' },
  { value: 'audio', labelKey: 'search.typeAudio' },
];

const SAMPLE_QUERY_KEYS = [
  'search.sample.dog',
  'search.sample.trip',
  'search.sample.rag',
  'search.sample.lease',
  'search.sample.mother',
];

function parseTypeFilter(value: string | null): SearchTypeFilter {
  return value === 'image' || value === 'audio' ? value : 'any';
}

function searchErrorText(error: unknown): string {
  if (error instanceof ApiException) return error.hint || error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

export function SearchPage() {
  const { t } = useT();
  const [params, setParams] = useSearchParams();
  const initialQ = params.get('q') ?? '';
  const [q, setQ] = React.useState(initialQ);
  const [type, setType] = React.useState<SearchTypeFilter>(
    parseTypeFilter(params.get('type')),
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

  const { data, isFetching, isError, error, refetch } = useSearch(
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

  // Record successful searches into history (once results land for a query).
  const history = useHistory('mem.history.search');
  React.useEffect(() => {
    if (hasQuery && data) history.push(debouncedQ);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, hasQuery, debouncedQ]);

  return (
    <div className="mx-auto max-w-6xl px-8 py-10">
      {/* Hero search */}
      <section className="mb-6">
        <Link
          to="/drive"
          className="inline-flex items-center gap-1.5 text-sm text-fg-muted hover:text-fg transition-colors mb-3"
        >
          <ArrowLeft className="h-3.5 w-3.5" /> {t('common.backToDrive')}
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">{t('search.title')}</h1>
        <p className="mt-1.5 text-sm text-fg-muted">{t('search.subtitle')}</p>
        <div className="mt-5 relative">
          <Input
            value={q}
            autoFocus
            onChange={(e) => setQ(e.target.value)}
            placeholder={t('search.placeholder')}
            leadingIcon={<Search />}
            trailing={isFetching ? <span className="text-2xs text-fg-subtle">…</span> : <Kbd>⏎</Kbd>}
            className="h-12 text-base"
          />
        </div>
        {!hasQuery && (
          <div className="mt-4 space-y-3">
            {history.items.length > 0 && (
              <div className="flex flex-wrap items-center gap-2">
                <span className="inline-flex items-center gap-1 text-xs text-fg-subtle mr-1">
                  <History className="h-3 w-3" /> {t('search.recent')}
                </span>
                {history.items.slice(0, 8).map((s) => (
                  <button
                    key={s}
                    onClick={() => setQ(s)}
                    className="group inline-flex items-center gap-1 rounded-full border border-border bg-bg-subtle
                               hover:bg-bg-inset hover:border-border-strong px-3 py-1 text-xs text-fg-muted
                               hover:text-fg transition-colors max-w-[16rem]"
                  >
                    <span className="truncate">{s}</span>
                    <X
                      className="h-3 w-3 opacity-0 group-hover:opacity-60 hover:!opacity-100"
                      onClick={(e) => {
                        e.stopPropagation();
                        history.remove(s);
                      }}
                    />
                  </button>
                ))}
                <button
                  onClick={history.clear}
                  className="text-2xs text-fg-subtle hover:text-fg underline-offset-2 hover:underline"
                >
                  {t('common.clear')}
                </button>
              </div>
            )}
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs text-fg-subtle mr-1">{t('search.try')}</span>
              {SAMPLE_QUERY_KEYS.map((key) => {
                const sample = t(key);
                return (
                  <button
                    key={key}
                    onClick={() => setQ(sample)}
                    className="rounded-full border border-border bg-bg-subtle hover:bg-bg-inset hover:border-border-strong
                             px-3 py-1 text-xs text-fg-muted hover:text-fg transition-colors"
                  >
                    {sample}
                  </button>
                );
              })}
            </div>
          </div>
        )}
      </section>

      {/* Filters */}
      <section className="mb-6 flex flex-wrap items-center gap-3 text-sm">
        <div className="flex items-center gap-1.5 text-fg-muted">
          <Filter className="h-3.5 w-3.5" />
          <span className="text-xs uppercase tracking-wider">{t('search.filter')}</span>
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
              {t(opt.labelKey)}
            </button>
          ))}
        </div>
        <DateField label={t('search.from')} value={since} onChange={setSince} />
        <DateField label={t('search.to')} value={until} onChange={setUntil} />
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
            {t('common.clear')}
          </Button>
        )}
      </section>

      {/* Query plan / meta */}
      {hasQuery && data?.query_plan?.entities && data.query_plan.entities.length > 0 && (
        <div className="mb-4 text-xs text-fg-muted flex items-center gap-2">
          <span>{t('search.detectedEntities')}</span>
          {data.query_plan.entities.map((e) => (
            <Badge key={e} tone="accent">
              {e}
            </Badge>
          ))}
        </div>
      )}

      {/* Results */}
      {!hasQuery ? (
        <div className="space-y-8">
          <PeopleSection onSearchPerson={(name) => setQ(name)} />
          <EmptyState
            icon={<Search />}
            title={t('search.start')}
            description={t('search.startHint')}
          />
        </div>
      ) : isFetching && !data ? (
        <ResultGridSkeleton />
      ) : isError ? (
        <EmptyState
          icon={<AlertTriangle />}
          title={t('search.failed')}
          description={
            <span>
              {t('search.failedHint')}
              <span className="mt-1 block font-mono text-2xs text-fg-subtle">
                {searchErrorText(error)}
              </span>
            </span>
          }
          action={
            <Button variant="secondary" size="sm" onClick={() => refetch()}>
              {t('common.retry')}
            </Button>
          }
        />
      ) : results.length === 0 ? (
        <EmptyState
          icon={<Search />}
          title={t('search.noHits')}
          description={t('search.noHitsHint')}
        />
      ) : (
        <ResultGrid results={results} />
      )}

      {/* Footer meta */}
      {hasQuery && data && (
        <div className="mt-10 text-2xs text-fg-subtle text-center">
          {t('search.footer', { total: data.total, ms: data._meta?.latency_ms ?? '?' })}
        </div>
      )}
    </div>
  );
}

/** "People" — face clusters folded into Search. Click a face to see that
 *  person's photos; name them inline so "photos with <name>" becomes searchable. */
function PeopleSection({ onSearchPerson }: { onSearchPerson: (name: string) => void }) {
  const { t } = useT();
  const [clusters, setClusters] = React.useState<FaceCluster[] | null>(null);
  const [selected, setSelected] = React.useState<FaceCluster | null>(null);
  const [files, setFiles] = React.useState<FaceFile[] | null>(null);
  const [nameDraft, setNameDraft] = React.useState('');

  React.useEffect(() => {
    listFaces()
      .then((r) => setClusters(r.clusters ?? []))
      .catch(() => setClusters([]));
  }, []);

  async function openPerson(c: FaceCluster) {
    setSelected(c);
    setNameDraft(c.name);
    setFiles(null);
    try {
      const r = await getFaceFiles(c.id);
      setFiles(r.files ?? []);
    } catch {
      setFiles([]);
    }
  }

  async function saveName() {
    if (!selected) return;
    const name = nameDraft.trim();
    await nameFace(selected.id, name).catch(() => {});
    setClusters((cs) => (cs ?? []).map((c) => (c.id === selected.id ? { ...c, name } : c)));
    setSelected((s) => (s ? { ...s, name } : s));
  }

  if (!clusters || clusters.length === 0) return null; // no people → nothing to show

  // Detail: one person's photos.
  if (selected) {
    return (
      <section>
        <button
          onClick={() => {
            setSelected(null);
            setFiles(null);
          }}
          className="mb-3 inline-flex items-center gap-1.5 text-sm text-fg-muted hover:text-fg"
        >
          <ArrowLeft className="h-3.5 w-3.5" /> {t('people.back')}
        </button>
        <div className="mb-4 flex items-center gap-3">
          <Avatar fileId={selected.cover_file_id} size={48} />
          <div className="flex items-center gap-2">
            <input
              value={nameDraft}
              onChange={(e) => setNameDraft(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && saveName()}
              placeholder={t('people.namePlaceholder')}
              className="h-9 w-48 rounded-md border border-border bg-bg-inset px-3 text-sm outline-none focus:border-accent/60"
            />
            <Button size="sm" variant="secondary" onClick={saveName}>
              {t('people.nameSaved')}
            </Button>
            {selected.name && (
              <Button size="sm" variant="ghost" onClick={() => onSearchPerson(selected.name)}>
                <Search className="h-3.5 w-3.5" /> {t('search.title')}
              </Button>
            )}
          </div>
        </div>
        {files === null ? (
          <div className="columns-2 md:columns-4 gap-3">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="mb-3 h-40 w-full" />
            ))}
          </div>
        ) : (
          <div className="columns-2 md:columns-4 gap-3 [column-fill:_balance]">
            {files.map((f) => (
              <Link
                key={f.file_id}
                to={`/files/${f.file_id}`}
                className="group mb-3 block break-inside-avoid overflow-hidden rounded-lg border border-border bg-bg-panel hover:border-border-strong"
              >
                <AuthedImage
                  fileId={f.file_id}
                  alt={f.name}
                  className="w-full h-auto object-contain"
                  fallback={
                    <div className="aspect-square grid place-items-center text-fg-subtle">
                      <ImageIcon className="h-6 w-6" />
                    </div>
                  }
                />
              </Link>
            ))}
          </div>
        )}
      </section>
    );
  }

  // Overview: avatar row of all people.
  return (
    <section>
      <div className="mb-2 flex items-baseline gap-2">
        <h3 className="text-xs uppercase tracking-wider text-fg-muted font-medium">
          {t('search.people')} · {clusters.length}
        </h3>
        <span className="text-2xs text-fg-subtle">{t('people.hint')}</span>
      </div>
      <div className="flex flex-wrap gap-4">
        {clusters.map((c) => (
          <button
            key={c.id}
            onClick={() => openPerson(c)}
            className="group flex w-20 flex-col items-center gap-1.5"
          >
            <Avatar fileId={c.cover_file_id} size={64} ring />
            <span className="max-w-full truncate text-xs text-fg">
              {c.name || t('people.unnamed')}
            </span>
            <span className="text-2xs text-fg-subtle">{t('people.photosN', { n: c.file_count })}</span>
          </button>
        ))}
      </div>
    </section>
  );
}

/** Round face avatar from a representative photo (full image, cropped to circle). */
function Avatar({ fileId, size, ring }: { fileId?: string; size: number; ring?: boolean }) {
  return (
    <div
      className={cn(
        'overflow-hidden rounded-full bg-bg-inset border border-border',
        ring && 'ring-2 ring-transparent group-hover:ring-accent/50 transition-all',
      )}
      style={{ width: size, height: size }}
    >
      {fileId ? (
        <AuthedImage
          fileId={fileId}
          className="h-full w-full object-cover"
          fallback={
            <div className="grid h-full w-full place-items-center text-fg-subtle">
              <ImageIcon className="h-5 w-5" />
            </div>
          }
        />
      ) : (
        <div className="grid h-full w-full place-items-center text-fg-subtle">
          <ImageIcon className="h-5 w-5" />
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
  const { t } = useT();
  // Split images vs docs/audio. Images go to a masonry-ish CSS columns layout;
  // docs/audio go to a list. Together they read like Linear's mixed views.
  const images = results.filter((r) => r.file.kind === 'image');
  const others = results.filter((r) => r.file.kind !== 'image');

  return (
    <div className="grid grid-cols-1 lg:grid-cols-[1fr_360px] gap-8 items-start">
      <section>
        <div className="mb-3 flex items-center justify-between">
          <h3 className="text-xs uppercase tracking-wider text-fg-muted font-medium">
            {t('search.visualResults')} · {images.length}
          </h3>
        </div>
        {images.length === 0 ? (
          <div className="text-xs text-fg-subtle py-6">{t('search.noImages')}</div>
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
          {t('search.docsAudio')} · {others.length}
        </h3>
        {others.length === 0 ? (
          <div className="text-xs text-fg-subtle py-6">{t('search.none')}</div>
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
  // Search scores are cosine similarity in [-1, 1]. Keep this a compact,
  // bounded relevance indicator rather than implying calibrated probability.
  const pct = Math.min(100, Math.max(0, Math.round(score * 100)));
  return (
    <Badge tone="muted" className="font-mono">
      {pct}
    </Badge>
  );
}

function ChannelBadge({ channel }: { channel: SearchResult['channel'] }) {
  const { t } = useT();
  if (!channel) return null;
  return (
    <Badge tone={channel === 'visual' ? 'accent' : 'neutral'}>
      {t(`search.channel.${channel}`)}
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
        {f.kind === 'image' ? (
          <AuthedImage
            fileId={f.id}
            alt={f.caption ?? f.name}
            className="w-full h-auto object-contain block transition-transform duration-500 group-hover:scale-[1.02]"
            fallback={
              <div className="aspect-[4/3] grid place-items-center text-fg-subtle">
                <ImageIcon className="h-8 w-8" />
              </div>
            }
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
            {result.snippet ?? f.caption ?? f.name}
          </div>
        </div>
      </div>
      <div className="space-y-2 px-3 py-2.5">
        <div className="truncate text-xs text-fg" title={f.name}>{f.name}</div>
        {result.snippet && (
          <div className="line-clamp-2 text-2xs leading-4 text-fg-muted">
            {result.snippet}
          </div>
        )}
        <div className="flex items-center gap-2">
          <div className="text-2xs text-fg-subtle flex-1 truncate">
            {f.timeline_at ? formatDate(f.timeline_at) : '—'}
          </div>
          <ChannelBadge channel={result.channel} />
          <ScoreBadge score={result.score} />
        </div>
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
          <span className="inline-flex flex-wrap items-center gap-2">
            {formatDate(f.timeline_at ?? f.created_at)}
            <ChannelBadge channel={result.channel} />
          </span>
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
