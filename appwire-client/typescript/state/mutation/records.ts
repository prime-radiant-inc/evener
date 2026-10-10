import type { ComposerMention } from "../../composerInput";
import type { SecureRandomSource } from "./secureUUID";
import { createSecureUUID, tryOrUndefined } from "./secureUUID";

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
// restore time. `presentationId` names one staging of the bytes: a host mints
// a new one whenever it stages bytes, so two attachments with the same
// presentationId carry the same bytes (the projection relies on this to tell
// a fresh read of a record from a change without reading the bytes).
export interface MutationAttachmentRef {
  presentationId: string;
  marker: number;
  name: string;
  mediaType: string;
}

// "canceled" is the durable record of a user's Stop: the row was never
// attempted, so cancellation is a fact about the client, not a guess about
// the daemon. Only an explicit user Retry releases it; no scan, reopen, or
// reclassification path may move it.
export type MutationOutboxState = "submitting" | "blockedUnknown" | "canceled";
export type MutationRecoveryKind = "rejected" | "orphaned";

// One submission as the client made it. The attachment type is a parameter so
// a host can carry its own bytes (the web's Blob) on the same record shape
// without that type reaching this module.
export interface MutationIntent<A extends MutationAttachmentRef = MutationAttachmentRef> {
  targetRef: string;
  threadId?: string;
  // The instance the target carried when this intent was made — the SAME
  // identity `expectedInstanceId` fences with (the model's instanceId, with
  // the thread id as its pre-instance fallback). Cleanup and Retry compare
  // this against the fused identity the current model presents, so a
  // replacement that rotates the instance while retaining the thread id is
  // still detected as a replacement. Optional because records written before
  // the field existed carry none, and such a row's identity falls back to its
  // threadId — exactly what a model with no instanceId presents.
  instanceId?: string;
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
  composerMentions?: readonly ComposerMention[];
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

// --- receipt settlement ------------------------------------------------------

// The accepted optimistic copy of a record a "pending" receipt settled: the
// one whitelist of fields the web IndexedDB adapter and the native adapter
// both build by hand, so the two can no longer drift. Every field is named
// explicitly rather than spread, because a source may be a recovery record
// (recoveryKind/recoveryReason) or an outbox record (attempted, its own
// state) and none of those is part of MutationOptimisticRecord: spreading one
// would leak the outbox-only `attempted` flag into the accepted row the
// optimistic table and its readers expect.
export function acceptedRecord<A extends MutationAttachmentRef = MutationAttachmentRef>(
  source: MutationRecord<A>,
): MutationOptimisticRecord<A> {
  return {
    version: source.version,
    clientMutationId: source.clientMutationId,
    // Provenance survives the outbox -> optimistic transition: dropping it
    // would make the accepted-but-unreflected mutation unattributed, and every
    // tab would claim it as its own send.
    originClientId: source.originClientId,
    intentSequence: source.intentSequence,
    createdAt: source.createdAt,
    targetRef: source.targetRef,
    threadId: source.threadId,
    // The enqueue-time instance rides the transition like provenance does:
    // dropping it would leave the accepted record identifying itself by
    // threadId alone, the pre-instance shape a replacement that retains the
    // thread id is invisible to.
    instanceId: source.instanceId,
    method: source.method,
    payload: source.payload,
    attachments: source.attachments,
    optimisticDisplay: source.optimisticDisplay,
    state: "accepted",
  };
}

// The "receipt carries input display" half of the rule both adapters use to
// decide whether a "pending" receipt keeps (or creates) the optimistic copy:
// the source record's display holds an input array the pending row renders.
// The receipt's own state (whether it is "pending" at all) is the caller's to
// check; a host may keep a copy for a reason beyond a display (the web keeps
// a notes/human/set copy so a retry lookup sees the accepted save as pending).
export function carriesOptimisticInput(record: { optimisticDisplay: unknown }): boolean {
  const display = record.optimisticDisplay;
  return display !== null && typeof display === "object" && "input" in display && Array.isArray(display.input);
}

// --- client provenance -------------------------------------------------------

// The store this client's identity is remembered in: per client, stable across
// that client's restarts. The web passes a lazy sessionStorage adapter (per
// tab, surviving a reload); a host without one passes `undefined` and gets a
// per-process identity that lives as long as the instance does. Structural on
// purpose — no DOM types here — and every access is guarded, because a
// storage can throw as easily as it can be missing.
export interface ClientIdentityStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export interface ClientIdentity {
  ownClientId(): string;
  isOwnMutationRecord(record: { originClientId?: string }): boolean;
}

const STORAGE_KEY = "evener-hub.mutation-client-identity";

// A fresh storeable identity, over the same secure-UUID helper mutation ids
// use: createSecureUUID already prefers randomUUID and falls back to an
// RFC4122-shaped id built from getRandomValues, or, lacking either, its own
// documented non-crypto id — so this module carries no second fallback of
// its own, and no default source: a host's random capability (globalThis.crypto,
// expo-crypto, none) is never this package's to assume.
function newClientIdentity(randomSource: SecureRandomSource): string {
  return `mutation-client-${createSecureUUID(randomSource)}`;
}

// One identity per instance, not a module singleton: the package names no
// browser global (sessionStorage is the web's, not every host's), and a host
// that builds a second instance over a second storage — a second tab's
// worker, a test double — must get a second identity rather than silently
// inheriting the first instance's.
//
// One client, one identity: once an instance has an identity — adopted from
// storage or generated — it is returned as-is and never replaced. In
// particular an identity created while storage was unavailable must survive
// storage becoming available later; reading storage again would switch
// identity mid-life and make records stamped with the first one look like
// another client's.
export function createClientIdentity(
  storage: ClientIdentityStorage | undefined,
  randomSource: SecureRandomSource,
): ClientIdentity {
  let identity: string | undefined;

  function ownClientId(): string {
    if (identity !== undefined) return identity;
    // Best-effort: the identity below keeps this client consistent either way.
    const stored = tryOrUndefined(() => storage?.getItem(STORAGE_KEY));
    if (stored !== null && stored !== undefined && stored !== "") {
      identity = stored;
      return stored;
    }
    const generated = newClientIdentity(randomSource);
    identity = generated;
    tryOrUndefined(() => storage?.setItem(STORAGE_KEY, generated));
    return generated;
  }

  // Whether a durable record belongs to this client: unattributed records
  // (written before the field existed) stay claimable so an in-flight
  // submission survives a deploy, but a record naming another client is
  // never ours.
  function isOwnMutationRecord(record: { originClientId?: string }): boolean {
    return record.originClientId === undefined || record.originClientId === ownClientId();
  }

  return { ownClientId, isOwnMutationRecord };
}
