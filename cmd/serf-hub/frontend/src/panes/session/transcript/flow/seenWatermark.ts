// Per-ref "seen" watermark (kata g2ez): the id of the last turn that
// existed the last time this session's pane was actually open, so a
// reopened session can mark where new content begins. Same per-ref
// localStorage precedent as composer/draft.ts (serf.composer.draft.v1.<ref>)
// - per-session scope, device-local persistence. Device-local is a real,
// accepted limitation (see useSeenDivider.ts's own doc comment): a laptop
// watermark never clears on a phone or second machine. A cross-device read
// cursor would need to live on the daemon - a materially bigger feature,
// out of scope here.
const STORAGE_PREFIX = "serf.transcript.seen.v1.";

export function seenWatermarkKey(ref: string): string {
  return `${STORAGE_PREFIX}${ref}`;
}

// Every localStorage access is guarded: private-mode/disabled/full storage
// degrades silently to "no watermark" rather than ever breaking the
// transcript, same convention as draft.ts's readDraft/writeDraft.
export function readSeenWatermark(ref: string): string | null {
  try {
    return localStorage.getItem(seenWatermarkKey(ref));
  } catch {
    return null;
  }
}

export function writeSeenWatermark(ref: string, turnId: string): void {
  try {
    localStorage.setItem(seenWatermarkKey(ref), turnId);
  } catch {
    // Best-effort: a full quota or Safari private-mode must never be fatal
    // to the transcript itself, only to the watermark surviving a reload.
  }
}
