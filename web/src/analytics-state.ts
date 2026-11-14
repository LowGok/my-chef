// Preference revisions stay in memory. Queued edits retain only their original
// revision; the client never receives an analytical subject or event identifier.
let revision: number | null = null;
export const analyticsRevision = () => revision;
export function clearAnalyticsRevision() {
  revision = null;
}
export function rememberAnalyticsRevision(value: number) {
  revision = Number.isSafeInteger(value) && value > 0 ? value : null;
}
export function queuedAnalyticsRevision(
  queue: ReadonlyArray<{ analyticsRevision?: number | null }>,
) {
  const first = queue[0]?.analyticsRevision;
  return first !== undefined &&
    first !== null &&
    first > 0 &&
    queue.every((op) => op.analyticsRevision === first)
    ? first
    : 0;
}
