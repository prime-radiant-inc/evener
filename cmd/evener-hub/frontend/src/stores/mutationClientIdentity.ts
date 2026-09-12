// The durable mutation outbox is shared per origin: every hub tab reads every
// other hub tab's records out of the same IndexedDB. "Did THIS client submit
// it" — the question send/queue routing asks (pendingReconcile's
// fromThisClient) — therefore needs a per-client identifier on the record, or
// one tab claims another tab's in-flight send and reroutes its own composer.
//
// sessionStorage holds it: per-tab, and stable across that tab's reloads. A
// reloaded page is the same client lineage for its own in-flight records —
// crash/reload recovery must still count them as "mine" for tier-6 routing —
// while a tab the user opens separately never shares the value. One residual
// is outside any application's control: the browser's own "duplicate tab"
// copies sessionStorage into the new tab with no opener to strip, so the
// duplicate shares this identity and the two tabs claim each other's sends -
// the same routing behavior the field's absence had. That residual is
// documented on the PR; the app-controlled opener-created-tab path (window.open
// without noopener) is closed. Storage access is guarded for environments
// without it; the fallback is held in module state so one page keeps one
// identity either way.

const STORAGE_KEY = "evener-hub.mutation-client-identity";

let fallbackIdentity: string | undefined;
let testIdentity: string | undefined;

/** A fresh storeable identity. crypto.randomUUID is the codebase's existing
 * strong identifier source (mutation ids use it); the random/timestamp string
 * remains the fallback for contexts that do not expose it. */
function newClientIdentity(): string {
  const uuid = globalThis.crypto?.randomUUID?.();
  if (uuid !== undefined) return `mutation-client-${uuid}`;
  return `mutation-client-${Math.random().toString(36).slice(2)}-${Date.now().toString(36)}`;
}

/** The identity of THIS client (page) for durable mutation provenance. Stamped
 * on every record this client enqueues and compared against records read back
 * out of the shared outbox. */
export function ownClientId(): string {
  if (testIdentity !== undefined) return testIdentity;
  try {
    const stored = globalThis.sessionStorage?.getItem(STORAGE_KEY);
    if (stored !== null && stored !== undefined && stored !== "") {
      // Cache the stored value too: if storage later throws (private mode, a
      // policy change), the fallback must keep this page's identity rather
      // than generate a new one that makes its in-flight records look foreign.
      fallbackIdentity = stored;
      return stored;
    }
  } catch {
    // Best-effort: the fallback below keeps this page's identity consistent.
  }
  fallbackIdentity ??= newClientIdentity();
  try {
    globalThis.sessionStorage?.setItem(STORAGE_KEY, fallbackIdentity);
  } catch {
    // Best-effort, same rationale.
  }
  return fallbackIdentity;
}

/** Whether a durable record belongs to this client: unattributed records
 * (written before the field existed) stay claimable so an in-flight send
 * survives a deploy, but a record naming another client is never ours. */
export function isOwnMutationRecord(record: { originClientId?: string }): boolean {
  return record.originClientId === undefined || record.originClientId === ownClientId();
}

// Test seam: simulates reading shared storage as a different tab. No
// production code may call this.
export function setMutationClientIdentityForTests(identity: string | undefined): void {
  testIdentity = identity;
}
