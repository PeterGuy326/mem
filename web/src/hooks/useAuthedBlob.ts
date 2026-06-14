import { useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { apiBlob } from '@/lib/api';

/**
 * Fetch a file's raw bytes (with bearer auth) and expose a short-lived object
 * URL usable as an `<img>` / `<audio>` src. The Blob is cached by React Query
 * (keyed on fileId) so re-mounting a thumbnail doesn't refetch; the object URL
 * is created per-consumer and revoked on unmount to avoid leaks.
 */
export function useAuthedBlobUrl(fileId: string | null | undefined, enabled = true) {
  const {
    data: blob,
    isLoading,
    isError,
  } = useQuery({
    queryKey: ['file-content', fileId],
    queryFn: () => apiBlob(`/files/${fileId}/content`),
    enabled: !!fileId && enabled,
    staleTime: Infinity,
    gcTime: 30 * 60 * 1000,
    retry: 1,
  });

  // Create the object URL inside the effect and revoke that exact URL on
  // cleanup. Doing it in useMemo + a separate cleanup revokes the live URL
  // under React StrictMode's double-invoked effects, producing broken images.
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    if (!blob) {
      setUrl(null);
      return;
    }
    const objectUrl = URL.createObjectURL(blob);
    setUrl(objectUrl);
    return () => URL.revokeObjectURL(objectUrl);
  }, [blob]);

  return { url, isLoading, isError };
}
