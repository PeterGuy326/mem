import * as React from 'react';
import { cn } from '@/lib/cn';
import { useAuthedBlobUrl } from '@/hooks/useAuthedBlob';

interface AuthedImageProps {
  /** File id; its raw bytes are fetched from /v1/files/{id}/content with auth. */
  fileId: string;
  alt?: string;
  className?: string;
  /** Rendered while loading or on error (e.g. a kind icon placeholder). */
  fallback?: React.ReactNode;
  /** Defer fetching until true (e.g. only when the tile is an image). */
  enabled?: boolean;
}

/**
 * `<img>` for an authenticated, token-protected file. The bytes can't be loaded
 * by a plain `src` (no Authorization header on img requests), so we fetch the
 * blob and hand the resulting object URL to the image.
 */
export function AuthedImage({ fileId, alt = '', className, fallback, enabled = true }: AuthedImageProps) {
  const { url, isLoading, isError } = useAuthedBlobUrl(fileId, enabled);

  if (url) {
    return (
      <img
        src={url}
        alt={alt}
        loading="lazy"
        draggable={false}
        className={cn('h-full w-full object-cover', className)}
      />
    );
  }
  if (isLoading) {
    return <div className={cn('h-full w-full animate-pulse bg-bg-inset', className)} />;
  }
  // isError or no data → fall back to the caller's placeholder.
  void isError;
  return <>{fallback ?? null}</>;
}
