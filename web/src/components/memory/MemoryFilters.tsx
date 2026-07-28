import * as React from 'react';
import { Filter, FolderSearch, RotateCcw } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { useT } from '@/i18n';
import { MEMORY_KINDS, type MemoryKind, type MemoryLifecycleFilter } from '@/lib/types';

export interface MemoryFilterValue {
  scope: string;
  kind?: MemoryKind;
  lifecycle: MemoryLifecycleFilter;
  pinned?: boolean;
}

export function MemoryFilters({
  value,
  onChange,
}: {
  value: MemoryFilterValue;
  onChange: (next: MemoryFilterValue) => void;
}) {
  const { t } = useT();
  const [scopeDraft, setScopeDraft] = React.useState(value.scope);

  React.useEffect(() => setScopeDraft(value.scope), [value.scope]);

  function update(patch: Partial<MemoryFilterValue>) {
    onChange({ ...value, ...patch });
  }

  function reset() {
    setScopeDraft('');
    onChange({ scope: '', lifecycle: 'active' });
  }

  return (
    <section
      className="surface grid gap-3 p-3 lg:grid-cols-[minmax(220px,1fr)_170px_180px_auto]"
      aria-label={t('memory.filters')}
    >
      <form
        className="flex min-w-0 gap-2"
        onSubmit={(event) => {
          event.preventDefault();
          update({ scope: scopeDraft.trim() });
        }}
      >
        <Input
          value={scopeDraft}
          onChange={(event) => setScopeDraft(event.target.value)}
          leadingIcon={<FolderSearch />}
          aria-label={t('memory.scope')}
          placeholder={t('memory.scopePlaceholder')}
          className="min-w-0"
        />
        <Button
          type="submit"
          size="sm"
          variant="secondary"
          disabled={scopeDraft.trim() === value.scope}
        >
          {t('memory.applyScope')}
        </Button>
      </form>

      <label className="grid gap-1">
        <span className="text-2xs uppercase tracking-wider text-fg-subtle">{t('memory.kind')}</span>
        <select
          value={value.kind ?? ''}
          onChange={(event) =>
            update({ kind: (event.target.value || undefined) as MemoryKind | undefined })
          }
          className="h-9 rounded-md border border-border bg-bg-inset px-2.5 text-xs text-fg outline-none focus:border-accent/60"
          aria-label={t('memory.kind')}
        >
          <option value="">{t('memory.allKinds')}</option>
          {MEMORY_KINDS.map((kind) => (
            <option key={kind} value={kind}>
              {t(`memory.kind.${kind}`)}
            </option>
          ))}
        </select>
      </label>

      <div className="grid gap-1">
        <span className="inline-flex items-center gap-1 text-2xs uppercase tracking-wider text-fg-subtle">
          <Filter className="h-3 w-3" />
          {t('memory.visibility')}
        </span>
        <div className="flex h-9 items-center rounded-md border border-border bg-bg-inset p-0.5">
          {(['active', 'archived', 'all'] as const).map((lifecycle) => (
            <button
              key={lifecycle}
              type="button"
              onClick={() => update({ lifecycle })}
              className={
                'h-7 flex-1 rounded px-2 text-2xs transition-colors ' +
                (value.lifecycle === lifecycle
                  ? 'bg-bg-panel text-fg shadow-soft'
                  : 'text-fg-muted hover:text-fg')
              }
              aria-pressed={value.lifecycle === lifecycle}
            >
              {t(`memory.lifecycle.${lifecycle}`)}
            </button>
          ))}
        </div>
      </div>

      <div className="flex items-end gap-2">
        <label className="grid min-w-28 flex-1 gap-1">
          <span className="text-2xs uppercase tracking-wider text-fg-subtle">
            {t('memory.pinFilter')}
          </span>
          <select
            value={value.pinned === undefined ? '' : String(value.pinned)}
            onChange={(event) =>
              update({
                pinned: event.target.value === '' ? undefined : event.target.value === 'true',
              })
            }
            className="h-9 rounded-md border border-border bg-bg-inset px-2.5 text-xs text-fg outline-none focus:border-accent/60"
            aria-label={t('memory.pinFilter')}
          >
            <option value="">{t('memory.pinAny')}</option>
            <option value="true">{t('memory.pinned')}</option>
            <option value="false">{t('memory.unpinned')}</option>
          </select>
        </label>
        <Button
          type="button"
          size="icon"
          variant="ghost"
          onClick={reset}
          aria-label={t('memory.resetFilters')}
          title={t('memory.resetFilters')}
        >
          <RotateCcw className="h-3.5 w-3.5" />
        </Button>
      </div>
    </section>
  );
}
