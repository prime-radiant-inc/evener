// The durable shape of a mutation this client submitted, and the one question
// every reader of a shared outbox has to answer about it: did THIS client
// submit it?
//
// A mutation outlives the call that made it. The web persists each one before
// dispatch and reads them back out of storage the whole origin shares, so the
// record is the evidence — of what was submitted, by whom, and how far it
// got — that a projection, a dispatcher and a recovery surface all read. Those
// readers are the same on any host; the storage under them is not (IndexedDB
// on the web, whatever native brings). This module is the shape and the
// provenance rule alone: no storage, no scheduling, no DOM.
//
// Attachment bytes are the one thing the shape cannot name portably (a Blob is
// a browser type, and a phone has no such thing), so a record carries the
// attachment's identifying metadata and each host extends it with whatever it
// actually stores — see MutationAttachmentRef.

// What every host can say about one staged attachment. `marker` is the
// composer marker number it was staged under: what pairs the attachment back
// to its "[image N]" anchor in composerText when a failed record is restored
// into a composer, recorded rather than re-derived from array position at
// restore time.
export interface MutationAttachmentRef {
  presentationId: string;
  marker: number;
  name: string;
  mediaType: string;
}

export type MutationOutboxState = "submitting" | "blockedUnknown";
export type MutationRecoveryKind = "rejected" | "orphaned";

// One submission as the client made it. The attachment type is a parameter so
// a host can carry its own bytes (the web's Blob) on the same record shape
// without that type reaching this module.
export interface MutationIntent<A extends MutationAttachmentRef = MutationAttachmentRef> {
  targetRef: string;
  threadId?: string;
  method: string;
  payload: Record<string, unknown>;
  attachments: A[];
  optimisticDisplay: unknown;
  // The composer's text exactly as typed, "[image N]" anchors intact. The
  // payload's own text is not a substitute: a composer translates every marker
  // to prose at the submit boundary, so a payload restored straight into a
  // composer would carry sentences about images in place of the anchors its
  // tiles remove. Absent on intents no composer authored. Canonical skill
  // selections need no sibling field here: they ride payload.input as
  // {type: "skill", name} items, so the outbox, optimistic and recovery
  // records all carry them in the one record they already persist.
  composerText?: string;
}

export interface MutationRecord<A extends MutationAttachmentRef = MutationAttachmentRef> extends MutationIntent<A> {
  version: 1;
  clientMutationId: string;
  // Which client (page, app instance) submitted this mutation. Storage is
  // shared per origin, so without it every reader claims every other reader's
  // records as its own — see isOwnMutationRecord. Optional because records
  // written before the field existed carry none, and unattributed records stay
  // claimable rather than losing their sender's routing mid-deploy.
  originClientId?: string;
  intentSequence: number;
  createdAt: number;
}

export interface MutationOutboxRecord<A extends MutationAttachmentRef = MutationAttachmentRef>
  extends MutationRecord<A> {
  state: MutationOutboxState;
  attempted?: boolean;
}

export interface MutationOptimisticRecord<A extends MutationAttachmentRef = MutationAttachmentRef>
  extends MutationRecord<A> {
  state: "accepted";
}

export interface MutationRecoveryRecord<A extends MutationAttachmentRef = MutationAttachmentRef>
  extends MutationOutboxRecord<A> {
  recoveryKind: MutationRecoveryKind;
  // Why the daemon refused, in its own words. Without it a recovery row can
  // only say that something did not happen: a Steer or Stop refused with
  // nothing on screen explaining why. Optional because records written before
  // this existed carry no reason, and because some recovery kinds (orphaned)
  // have no daemon message to carry.
  recoveryReason?: string;
}

// --- client provenance -------------------------------------------------------

// The store this client's identity is remembered in: per client, stable across
// that client's restarts. The web passes sessionStorage (per tab, surviving a
// reload); a host without one passes nothing and gets a per-process identity
// that lives as long as the module does. Structural on purpose — no DOM types
// here — and every access is guarded, because a storage can throw as easily as
// it can be missing.
export interface ClientIdentityStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

const STORAGE_KEY = "evener-hub.mutation-client-identity";

let identity: string | undefined;

function defaultStorage(): ClientIdentityStorage | undefined {
  const storage = (globalThis as { sessionStorage?: ClientIdentityStorage }).sessionStorage;
  return storage ?? undefined;
}

// A fresh storeable identity. crypto.randomUUID is this codebase's strong
// identifier source (mutation ids use it); the random/timestamp string remains
// the fallback for runtimes that do not expose it.
function newClientIdentity(): string {
  const uuid = globalThis.crypto?.randomUUID?.();
  if (uuid !== undefined) return `mutation-client-${uuid}`;
  return `mutation-client-${Math.random().toString(36).slice(2)}-${Date.now().toString(36)}`;
}

// The identity of THIS client for durable mutation provenance: stamped on
// every record it enqueues, and compared against records read back out of
// shared storage.
//
// One client, one identity: once this module has an identity — adopted from
// storage or generated — it is returned as-is and never replaced. In
// particular an identity created while storage was unavailable must survive
// storage becoming available later; reading storage again would switch
// identity mid-life and make records stamped with the first one look like
// another client's.
export function ownClientId(storage: ClientIdentityStorage | undefined = defaultStorage()): string {
  if (identity !== undefined) return identity;
  try {
    const stored = storage?.getItem(STORAGE_KEY);
    if (stored !== null && stored !== undefined && stored !== "") {
      identity = stored;
      return stored;
    }
  } catch {
    // Best-effort: the identity below keeps this client consistent either way.
  }
  identity = newClientIdentity();
  try {
    storage?.setItem(STORAGE_KEY, identity);
  } catch {
    // Best-effort, same rationale.
  }
  return identity;
}

// Whether a durable record belongs to this client: unattributed records
// (written before the field existed) stay claimable so an in-flight submission
// survives a deploy, but a record naming another client is never ours.
export function isOwnMutationRecord(record: { originClientId?: string }): boolean {
  return record.originClientId === undefined || record.originClientId === ownClientId();
}

// Test seam: simulates reading shared storage as a different client. No
// production code may call this.
export function setMutationClientIdentityForTests(value: string | undefined): void {
  identity = value;
}
