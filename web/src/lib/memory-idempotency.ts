/**
 * The same authenticated actor retrying the same target state gets a stable
 * key. A different actor gets a different namespace, so their request cannot
 * be mistaken for the first actor's replay.
 */
export function memoryActionKey(
  actorID: string,
  memoryID: string,
  expectedVersion: number,
  action: string,
): string {
  const actor = actorID.trim();
  if (!actor) throw new Error('authenticated actor id is required');
  return [
    'web-memory',
    encodeURIComponent(actor),
    encodeURIComponent(memoryID),
    `v${expectedVersion}`,
    encodeURIComponent(action),
  ].join(':');
}
