import { Badge } from '@/components/ui/Badge';
import { useT } from '@/i18n';
import type { HandoffReference } from '@/lib/types';
import { ReferenceLink } from './ReferenceLink';

export function ReferenceList({ references }: { references: HandoffReference[] }) {
  const { t } = useT();
  if (references.length === 0) {
    return <div className="text-xs text-fg-subtle">{t('task.noReferences')}</div>;
  }

  return (
    <ol className="divide-y divide-border">
      {references.map((reference) => (
        <li key={`${reference.ordinal}-${reference.uri}`} className="px-4 py-3">
          <div className="mb-1.5 flex flex-wrap items-center gap-2">
            <span className="font-mono text-2xs text-fg-subtle">
              {String(reference.ordinal + 1).padStart(2, '0')}
            </span>
            <Badge tone="neutral">{reference.relation}</Badge>
            {reference.required && <Badge tone="warn">{t('task.required')}</Badge>}
          </div>
          <ReferenceLink uri={reference.uri} />
          {reference.expected_sha256 && (
            <div className="mt-1.5 break-all font-mono text-2xs text-fg-subtle">
              expected:{reference.expected_sha256}
            </div>
          )}
        </li>
      ))}
    </ol>
  );
}
