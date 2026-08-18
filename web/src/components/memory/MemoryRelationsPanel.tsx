import { useQuery } from '@tanstack/react-query';
import { ArrowDownLeft, ArrowUpRight, Loader2 } from 'lucide-react';
import { Link, useLocation } from 'react-router';
import { Badge, type BadgeProps } from '@/components/ui/Badge';
import { memoryKeys, useMemoryRelations } from '@/hooks/useMemories';
import { useWorkspaceQueryKey } from '@/hooks/useWorkspace';
import { useT } from '@/i18n';
import { getMemory } from '@/lib/memories';
import { formatDateTime, truncateMiddle } from '@/lib/format';
import type { MemoryRelation, MemoryRelationType } from '@/lib/types';

const OUTBOUND_PARAMS = { direction: 'source' } as const;
const INBOUND_PARAMS = { direction: 'target' } as const;

const RELATION_TONES: Record<MemoryRelationType, NonNullable<BadgeProps['tone']>> = {
  supersedes: 'warn',
  corrects: 'accent',
  occurrence_of: 'neutral',
};

/**
 * Resolves the peer memory of a relation edge through the existing detail
 * endpoint. Unreadable peers (gone, forgotten, or out of path scope) degrade
 * to a plain label instead of breaking the chain view.
 */
function RelationPeer({ peerID }: { peerID: string }) {
  const { t } = useT();
  const location = useLocation();
  const workspaceID = useWorkspaceQueryKey();
  const peer = useQuery({
    queryKey: memoryKeys.detail(workspaceID, peerID),
    queryFn: () => getMemory(peerID),
    retry: false,
  });

  const label = peer.data
    ? truncateMiddle(peer.data.content.replace(/\s+/g, ' ').trim() || peerID, 64)
    : peer.isError
      ? `${t('memories.relation.peerUnavailable')} · ${truncateMiddle(peerID, 17)}`
      : truncateMiddle(peerID, 17);

  return (
    <Link
      to={`/memories/${encodeURIComponent(peerID)}${location.search}`}
      className="min-w-0 truncate text-xs text-accent hover:text-accent-hover"
      title={peer.data ? peer.data.content : peerID}
    >
      {label}
    </Link>
  );
}

function RelationRow({
  relation,
  direction,
}: {
  relation: MemoryRelation;
  direction: 'outbound' | 'inbound';
}) {
  const { t } = useT();
  const peerID = direction === 'outbound' ? relation.target_id : relation.source_id;
  const Icon = direction === 'outbound' ? ArrowUpRight : ArrowDownLeft;
  return (
    <li className="min-w-0 py-1.5">
      <div className="flex min-w-0 items-center gap-1.5">
        <Icon className="h-3 w-3 flex-none text-fg-subtle" aria-hidden />
        <Badge tone={RELATION_TONES[relation.relation_type] ?? 'neutral'}>
          {t(`memories.relationType.${relation.relation_type}`)}
        </Badge>
        <RelationPeer peerID={peerID} />
        <time
          dateTime={relation.created_at}
          className="ml-auto flex-none font-mono text-2xs text-fg-subtle"
        >
          {formatDateTime(relation.created_at)}
        </time>
      </div>
      {relation.reason && (
        <p className="mt-1 pl-[18px] text-2xs leading-4 text-fg-subtle">
          {t('memories.relation.reason')}: {relation.reason}
        </p>
      )}
    </li>
  );
}

/**
 * Read-only view of both directions of a memory's immutable relation edges
 * (who superseded/corrected whom). Reused by the ledger row expansion and the
 * detail page card.
 */
export function MemoryRelationsPanel({ memoryID }: { memoryID: string }) {
  const { t } = useT();
  const outbound = useMemoryRelations(memoryID, OUTBOUND_PARAMS);
  const inbound = useMemoryRelations(memoryID, INBOUND_PARAMS);

  if (outbound.isError || inbound.isError) {
    return <p className="text-xs text-danger">{t('memories.relationsFailed')}</p>;
  }
  if (outbound.isPending || inbound.isPending) {
    return (
      <span className="inline-flex items-center gap-1.5 text-xs text-fg-subtle">
        <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />
        {t('memories.relationsLoading')}
      </span>
    );
  }

  const outboundRelations = outbound.data ?? [];
  const inboundRelations = inbound.data ?? [];
  if (outboundRelations.length === 0 && inboundRelations.length === 0) {
    return <p className="text-xs text-fg-subtle">{t('memories.relationsEmpty')}</p>;
  }

  return (
    <div className="grid gap-2">
      {outboundRelations.length > 0 && (
        <div className="min-w-0">
          <div className="text-2xs uppercase tracking-wider text-fg-subtle">
            {t('memories.relations.outbound')}
          </div>
          <ul className="mt-1 divide-y divide-border/60">
            {outboundRelations.map((relation) => (
              <RelationRow key={relation.id} relation={relation} direction="outbound" />
            ))}
          </ul>
        </div>
      )}
      {inboundRelations.length > 0 && (
        <div className="min-w-0">
          <div className="text-2xs uppercase tracking-wider text-fg-subtle">
            {t('memories.relations.inbound')}
          </div>
          <ul className="mt-1 divide-y divide-border/60">
            {inboundRelations.map((relation) => (
              <RelationRow key={relation.id} relation={relation} direction="inbound" />
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
