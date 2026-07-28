import { ExternalLink, FileText, Fingerprint } from 'lucide-react';
import { Link } from 'react-router-dom';
import { cn } from '@/lib/cn';

export function memFileID(uri: string): string | null {
  try {
    const parsed = new URL(uri);
    if (parsed.protocol !== 'mem:' || parsed.hostname.toLowerCase() !== 'files') return null;
    const id = decodeURIComponent(parsed.pathname.replace(/^\/+/, '')).split('/')[0];
    return id || null;
  } catch {
    return null;
  }
}

export function ReferenceLink({
  uri,
  className,
  compact = false,
}: {
  uri: string;
  className?: string;
  compact?: boolean;
}) {
  const fileID = memFileID(uri);
  const baseClass = cn(
    'inline-flex min-w-0 items-center gap-1.5 text-xs font-mono',
    'text-fg-muted hover:text-accent transition-colors',
    className,
  );
  const text = (
    <span className={cn('break-all', compact && 'truncate')} title={uri}>
      {uri}
    </span>
  );

  if (fileID) {
    return (
      <Link to={`/files/${encodeURIComponent(fileID)}`} className={baseClass}>
        <FileText className="h-3.5 w-3.5 flex-none" />
        {text}
      </Link>
    );
  }

  if (/^https?:\/\//i.test(uri)) {
    return (
      <a href={uri} target="_blank" rel="noreferrer" className={baseClass}>
        <ExternalLink className="h-3.5 w-3.5 flex-none" />
        {text}
      </a>
    );
  }

  return (
    <span className={cn(baseClass, 'hover:text-fg-muted')}>
      <Fingerprint className="h-3.5 w-3.5 flex-none" />
      {text}
    </span>
  );
}
