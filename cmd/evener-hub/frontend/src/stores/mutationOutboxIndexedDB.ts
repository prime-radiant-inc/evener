import type { ComposerMention, InputItem } from "@evener/appwire-client";
import {
  canEditQueuedMessage,
  canRemoveQueuedMessage,
  matchesQueuedMessageSnapshot,
  type WebQueuedMessageStorage,
} from "@evener/appwire-client/state/mutation";
import { IDBConnection, requestResult, transactionCompletion, tryAbortTransaction } from "./idbConnection";
import { ownClientId } from "./mutationClientIdentity";
import type {
  MutationAttachment,
  MutationIntent,
  MutationOptimisticRecord,
  MutationOutboxRecord,
  MutationRecord,
  MutationRecoveryKind,
  MutationRecoveryRecord,
  MutationRetryCapture,
  MutationStopBarrier,
  MutationStopCapture,
} from "./mutationOutbox";
import { acceptedRecord, effectiveWireId, retainsAcceptedCopy, settlementRetiresRecovery } from "./mutationOutbox";
import { trackProjectionWork } from "./projectionWork";
import { createSecureUUID } from "./secureUUID";

type MutationOutboxOperation =
  | "markAttempted"
  | "unmarkAttempted"
  | "enqueueIntent"
  | "enqueueInterruptAndCancel"
  | "cancelUnattempted"
  | "discardCanceled"
  | "mintRetry"
  | "settleReceipt"
  | "transferToRecovery"
  | "updateRecoveryInput"
  | "preserveRecoveryComposition"
  | "discardRecovery"
  | "resendRecovery"
  | "editQueuedMessage"
  | "removeQueuedMessage"
  | "retargetOutbox";

// A canonical deep-equal for JSON-shaped mutation payloads: object key
// order is not part of a payload's meaning (the daemon hashes a canonical
// serialization), so the comparison sorts keys at every depth.
function canonicalJson(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalJson);
  if (typeof value === "object" && value !== null) {
    const record = value as Record<string, unknown>;
    const sorted: Record<string, unknown> = {};
    for (const key of Object.keys(record).sort()) sorted[key] = canonicalJson(record[key]);
    return sorted;
  }
  return value;
}
function sameJsonShape(left: unknown, right: unknown): boolean {
  return JSON.stringify(canonicalJson(left)) === JSON.stringify(canonicalJson(right));
}

type VersionedMutationRecoveryRecord = MutationRecoveryRecord & {
  compositionRevision?: number;
  // Set only by a refusal that never reached the daemon (the hub refused the
  // request's shape before any execution, so the daemon journaled nothing for
  // the wire): the wire may still be applied by an outstanding attempt, and
  // the recovery's resend must INHERIT the wire identity so the daemon's
  // journal replays instead of applying the composition twice.
  wireDeliveryUncertain?: boolean;
  // The wire's JOURNALED composition, frozen at transferToRecovery time on a
  // delivery-uncertain recovery. The resend's byte-identity is judged against
  // this immutable copy, never the record's mutable payload: the composer's
  // autosave replaces payload.input in place, and the Send path itself
  // persists before resending, so the current payload - and the
  // compositionRevision every such persist bumps - cannot tell an edit from
  // a no-op save. Absent on journaled-rejected recoveries (fresh ids there)
  // and on legacy marked rows written before this field existed, which fail
  // closed to a fresh id.
  originalWirePayload?: Record<string, unknown>;
};

// A stale row that still holds exactly the composition a refused pane meant to
// write already preserves it: duplicating it would give the same unsent work
// two recovery owners.
function sameRecoveryComposition(
  record: MutationRecoveryRecord,
  input: readonly InputItem[],
  composerText: string,
  composerMentions: readonly ComposerMention[] | undefined,
  attachments: readonly MutationAttachment[],
): boolean {
  return (
    record.composerText === composerText &&
    JSON.stringify(record.payload.input ?? []) === JSON.stringify(input) &&
    JSON.stringify(record.composerMentions ?? []) === JSON.stringify(composerMentions ?? []) &&
    // Markers pair an image with its "[image N]" anchor, so two rows with the
    // same bytes and text but different associations are not the same
    // composition. Presentation IDs name a staging of those bytes, not the
    // composition, and are deliberately excluded.
    JSON.stringify(record.attachments.map(({ marker, name, mediaType }) => ({ marker, name, mediaType }))) ===
      JSON.stringify(attachments.map(({ marker, name, mediaType }) => ({ marker, name, mediaType })))
  );
}

export type MutationRecoveryResendResult =
  // retiredIds: the outbox rows the consumption retired (every other row of
  // a consumed recovery's wire - the caller resolves their in-memory
  // delivery registrations in the same breath).
  | { status: "sent"; record: MutationOutboxRecord; retiredIds: string[] }
  | { status: "consumed" }
  | { status: "refused" };

// What retargetOutbox moved to the resumed ref, by store: the caller drives its
// per-ref bookkeeping when ANY of them moved (an optimistic-only or
// recovery-only identity change still needs the new ref pinned and armed).
export interface RetargetedMutations {
  outbox: MutationOutboxRecord[];
  optimistic: MutationOptimisticRecord[];
  recovery: MutationRecoveryRecord[];
}

export interface MutationOutboxIndexedDBOptions {
  indexedDB?: IDBFactory;
  databaseName?: string;
  createMutationId?: () => string;
  createPresentationId?: () => string;
  now?: () => number;
  onWriteStalled?: (waiting: boolean) => void;
  // Storage-fault seam used to prove IndexedDB rollback at commit boundaries.
  beforeCommit?: (operation: MutationOutboxOperation) => void;
  // Diagnostic sink for the outbox database's open/watchdog/upgrade/retire
  // failure paths. Purely observational: a throwing reporter cannot change the
  // storage outcome. Defaults to one console.warn line with a stable prefix.
  onOpenDiagnostic?: (diagnostic: MutationOutboxOpenDiagnostic) => void;
}

// The displayed-ref half a recovery Send's press must name, beyond the base
// capture pair (MutationStopCapture): the ref this pane renders, whose Stop
// epoch the resend fences alongside the destination's - after a peer
// retarget the two differ, and a Stop on the still-displayed session names
// the displayed ref, so the resend stamps it on the row it enqueues.
export type DisplayedRecoveryStopCapture = MutationStopCapture & { displayedRef: string };

const DATABASE_NAME = "evener-mutation-outbox";
// Version 5 is a compatibility fence, not a schema migration: the upgrade
// handler below is unchanged and purely additive. The deployed version-4 code
// fences only at the seams around dispatch, so a mixed-version share would
// let the older tab's markAttempted commit attempt evidence on - and then
// redispatch - a row a newer Stop crossed, while its cancel scan misses
// displayed-ref rows entirely (the original #5182 High again,
// mixed-version); it also re-enables parked rows in place, so it could
// reopen a "superseded" row this build settles terminal at a mint, and its
// markUnknown would reclassify one back to blockedUnknown - reopening a path
// around the explicit-Retry mint entirely. The older eras' hazards (uncounted
// attempt evidence, a canceled row stalling an old tab's FIFO) sit below it
// and stay fenced by the same bump. A version-4 open against this database
// fails with VersionError, and the old tab fails closed (storage-unavailable
// errors) instead of silently misfencing: the safe mixed-version state is a
// reload, never a silent share (docs/design/stop-cancellation-outbox.md §8).
// The schema-fence version, exported so tests assert against the production
// constant rather than a literal a future bump strands.
export const DATABASE_VERSION = 5;
const OUTBOX_STORE = "outbox";
const OPTIMISTIC_STORE = "optimistic";
const RECOVERY_STORE = "recovery";
const SEQUENCE_STORE = "sequences";
const TARGET_SEQUENCE_INDEX = "byTargetSequence";
const STORAGE_WAIT_MS = 10_000;

// The unresolved-attempt baseline a record's own metadata proves. A missing
// count on a pre-count record reads as the one unresolved attempt its
// attempted boolean claims. A record with neither field is a pre-#936 row,
// persisted untouched through every later schema version, whose delivery
// state from that era is unknown - the Stop guard's own rule for these rows
// - so it keeps one unresolved attempt as its baseline: only an explicit
// `attempted: false` proves no earlier attempt.
function priorUnresolvedAttempts(record: { unresolvedAttempts?: number; attempted?: boolean }): number {
  if (record.unresolvedAttempts !== undefined) return record.unresolvedAttempts;
  if (record.attempted === undefined) return 1;
  return record.attempted ? 1 : 0;
}

// The store scope every enqueue transaction opens: the three active record
// stores plus the sequence store. All three record stores are locked so the
// cross-store clientMutationId uniqueness check (#assertMutationIdAvailable)
// reads and writes under one transaction.
const ENQUEUE_STORES = [OUTBOX_STORE, OPTIMISTIC_STORE, RECOVERY_STORE, SEQUENCE_STORE];

// The outbox's own storage failures: an open the database refused or that
// another tab's older schema blocked, or a connection superseded mid-open.
// Callers tell storage failing from a rejected record by this brand (and by
// IndexedDB's own DOMException names), never by message.
export class MutationStorageError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "MutationStorageError";
  }
}

// The one neutral message for a transaction that timed out. The same error
// covers reads, Stop, cancel, and enqueue writes, so the sentence must read
// after any action headline ("Send failed: ...", "Stop failed: ...") and name
// neither a draft nor a cause. It is a bare retryable detail, never a gate - a
// stuck open is not remembered, so every later operation still attempts the
// open and a send after the open recovers succeeds.
export class MutationStorageTimeoutError extends MutationStorageError {
  constructor() {
    super("It didn't go through. Try again.");
    this.name = "MutationStorageTimeoutError";
  }
}

// The four failure-prone paths on the outbox database. These are the open
// failures that can precede the wedged Chromium connection coordinator
// (crbug 40278488), where open() fires neither success, error, nor blocked, so
// a later recurrence is only visible through what was recorded on the way in.
// Observation only: nothing here changes the storage outcome.
export type MutationOutboxOpenDiagnosticPath =
  | "open-timeout"
  | "open-blocked"
  | "upgrade-abandoned"
  | "versionchange-retire";

export interface MutationOutboxOpenDiagnostic {
  // The database the path ran against, and its schema version fence.
  database: string;
  version: number;
  path: MutationOutboxOpenDiagnosticPath;
  // True when an open's schema-upgrade (versionchange) transaction was live at
  // the moment the record was made: always true for an upgradeneeded handler,
  // true for a timeout that lands while the upgrade is still open, false once
  // the upgrade has committed or for the blocked and retire paths.
  versionchangeTransaction: boolean;
}

// The default sink: one console line with a stable prefix, greppable from a
// device log. It is the only place that knows about the console, so tests
// inject their own reporter and assert on records, not console text.
export function warnOpenDiagnostic(diagnostic: MutationOutboxOpenDiagnostic): void {
  console.warn("evener mutation outbox:", diagnostic);
}

// A real run - the dev server or a built bundle - gets the console line; the
// unit-test process does not, because tests install their own reporter and the
// shared console guard treats any unspied output as a failure. This is the same
// test-aware branch AppShell and ConnectionBanner already use, and it changes
// no storage behavior.
export const DEFAULT_OPEN_DIAGNOSTIC: (diagnostic: MutationOutboxOpenDiagnostic) => void =
  import.meta.env.MODE === "test" ? () => {} : warnOpenDiagnostic;

interface TargetSequence {
  targetRef: string;
  lastSequence: number;
  // The ref's durable stop epoch (§4's stop barrier): bumped inside every
  // Stop's cancel transaction and compared by an enqueue carrying a click-time
  // capture. Rides the sequence row so the barrier needs no schema change.
  stopEpoch?: number;
}

// The rows a Stop may honestly cancel: still waiting ("submitting" or
// "blockedUnknown") and proven unattempted by the flag the dispatcher's
// pre-transport write sets. An attempted row may already be on the wire; it is
// reported in-flight/uncertain, not canceled. A row missing the flag entirely -
// the pre-#936 shape, before markAttempted existed - has unknown attempt state
// too, so it gets the same conservative treatment rather than a cancellation
// the client cannot honor.
function isCancelableByStop(record: MutationOutboxRecord): boolean {
  return (record.state === "submitting" || record.state === "blockedUnknown") && record.attempted === false;
}

// MutationOutboxIndexedDB serializes sequence allocation with each durable
// transition. Cross-tab correctness comes from IndexedDB readwrite transaction
// ordering rather than a browser lease or leader election.
export class MutationOutboxIndexedDB implements WebQueuedMessageStorage<MutationAttachment> {
  readonly #connection: IDBConnection;
  readonly #databaseName: string;
  readonly #createMutationId: () => string;
  readonly #createPresentationId: () => string;
  readonly #now: () => number;
  readonly #beforeCommit: ((operation: MutationOutboxOperation) => void) | undefined;
  readonly #onWriteStalled: ((waiting: boolean) => void) | undefined;
  readonly #onOpenDiagnostic: (diagnostic: MutationOutboxOpenDiagnostic) => void;
  #supersededDiscardListener: ((targetRef: string) => void) | undefined;
  #settledListener: ((targetRef: string) => void) | undefined;
  // The wire sweep's retired ids, fired inside the settlement itself so a
  // host can resolve in-memory bookkeeping synchronously - before any
  // trailing read could fail and leave it armed on retired rows.
  #retiredIdsListener: ((retiredIds: string[]) => void) | undefined;
  #stalledWrites = 0;

  constructor(options: MutationOutboxIndexedDBOptions = {}) {
    const factory = options.indexedDB ?? globalThis.indexedDB;
    if (!factory) throw new Error("IndexedDB is unavailable");
    this.#databaseName = options.databaseName ?? DATABASE_NAME;
    this.#createMutationId = options.createMutationId ?? createSecureUUID;
    this.#createPresentationId = options.createPresentationId ?? createSecureUUID;
    this.#now = options.now ?? Date.now;
    this.#beforeCommit = options.beforeCommit;
    this.#onWriteStalled = options.onWriteStalled;
    this.#onOpenDiagnostic = options.onOpenDiagnostic ?? DEFAULT_OPEN_DIAGNOSTIC;
    this.#connection = new IDBConnection({
      indexedDB: factory,
      databaseName: this.#databaseName,
      databaseVersion: DATABASE_VERSION,
      waitMs: STORAGE_WAIT_MS,
      upgrade: (database) => this.#upgrade(database),
      errors: {
        open: () => new MutationStorageError("Unable to open mutation outbox"),
        superseded: () => new MutationStorageError("Mutation outbox connection was closed"),
        timeout: () => new MutationStorageTimeoutError(),
        blocked: () => new MutationStorageError("Mutation outbox upgrade is blocked"),
      },
      reportDiagnostic: (path, active) => this.#reportOpenDiagnostic(path, active),
    });
  }

  close(): void {
    this.#connection.close();
  }

  // §6's note-row supersede discard (below) is the one write this class
  // performs fire-and-forget: it commits after the settle that spawned it has
  // already notified, so without a listener of its own its removal would be
  // invisible to the owning runtime's projections and pins until some
  // unrelated storage event happens to fire. The owning runtime registers
  // here; the listener fires when the cleanup's write completes, zero
  // deletions included (another tab may have removed the rows first — this
  // tab's cached projection and pin are exactly what zero leaves stale). A
  // failed write stays silent: the rows remain for the next settle, clear,
  // or delete, the discard's own best-effort boundary.
  setSupersededDiscardListener(listener: ((targetRef: string) => void) | undefined): void {
    this.#supersededDiscardListener = listener;
  }

  // Every successful settle, with the settled record's own targetRef: the
  // record that proved delivery is the one authority on which ref's state
  // changed, so callers do not have to derive it from the row id.
  setSettledListener(listener: ((targetRef: string) => void) | undefined): void {
    this.#settledListener = listener;
  }

  setRetiredIdsListener(listener: ((retiredIds: string[]) => void) | undefined): void {
    this.#retiredIdsListener = listener;
  }

  async enqueueIntent(intent: MutationIntent, barrier?: MutationStopBarrier): Promise<MutationOutboxRecord> {
    if (!intent.targetRef.trim()) throw new Error("targetRef is required");
    return this.#write(ENQUEUE_STORES, "enqueueIntent", async (transaction) => {
      // §4's stop barrier: a Stop whose cancel transaction committed while
      // this submission was in flight - after the click, before this write
      // issued - has left the ref's durable stop epoch past the click-time
      // capture. The row commits born-"canceled": announced for the canceled
      // queue strip, never dispatched, released only by an explicit Retry.
      const stopEpoch = await this.#stopEpochOf(transaction, intent.targetRef);
      const canceledByBarrier = barrier !== undefined && stopEpoch > barrier.stopEpoch;
      const intentSequence = await this.#allocateSequence(transaction, intent.targetRef);
      const clientMutationId = this.#createMutationId();
      await this.#assertMutationIdAvailable(transaction, clientMutationId);
      const record: MutationOutboxRecord = {
        ...intent,
        payload: { ...intent.payload, clientMutationId },
        version: 1,
        clientMutationId,
        originClientId: ownClientId(),
        intentSequence,
        createdAt: this.#now(),
        state: canceledByBarrier ? "canceled" : "submitting",
        attempted: false,
        // The barrier's own read, stored: the refusal rollback's fence
        // compares the ref's epoch at rollback time against this capture.
        stopEpochAtEnqueue: stopEpoch,
      };
      await requestResult(transaction.objectStore(OUTBOX_STORE).add(record));
      return record;
    });
  }

  // The click-time half of §4's stop barrier: what an enqueuing tab reads at
  // its user's click, before its durable write issues. Fresh from the
  // ref's sequence row, never cached - another tab's Stop is invisible to
  // this tab's memory.
  //
  // This read is the click's first storage observation, and it is never
  // retried: a retry could answer later with an epoch a Stop had since bumped,
  // which is exactly the fence this capture exists to hold. A timeout rejects
  // it - a composer send then proceeds through threads.ts's direct fallback
  // with no click-time epoch, while every other durable write fails closed.
  async readStopEpoch(targetRef: string): Promise<number> {
    return this.#read(SEQUENCE_STORE, (transaction) => this.#stopEpochOf(transaction, targetRef));
  }

  async getOutbox(clientMutationId: string): Promise<MutationOutboxRecord | undefined> {
    return this.#read(OUTBOX_STORE, async (transaction) => {
      return requestResult<MutationOutboxRecord | undefined>(
        transaction.objectStore(OUTBOX_STORE).get(clientMutationId),
      );
    });
  }

  // The Retry click's capture, §4's stop barrier on the release path: the row
  // AND the ref's stop epoch read in ONE readonly transaction, so the release
  // can compare against a snapshot no cross-tab Stop can slip inside. The
  // caller REQUESTS this read in the click's own synchronous prefix - the
  // enqueue barrier's own rule - because the ref is unknown until the row is
  // read: the capture rides the very read that tells the click which ref it
  // is retrying on, making it the click's first storage observation. Both
  // requests issue synchronously, before either is awaited: the pair is the
  // transaction's whole workload from creation, a readonly transaction reads
  // one consistent snapshot for both stores, and no continuation can ever
  // issue a request on a transaction that auto-committed underneath a held
  // event delivery (the retry-lookup seams the note-draft tests hold).
  async getOutboxWithStopEpoch(
    clientMutationId: string,
  ): Promise<{ record: MutationOutboxRecord | undefined; stopEpoch: number; displayedStopEpoch: number }> {
    return this.#rowWithStopEpoch(OUTBOX_STORE, clientMutationId, (record) => record?.displayedRef);
  }

  // A recovery Send's press-time capture, the recovery-row twin of
  // getOutboxWithStopEpoch above: the row, its durable destination's stop
  // epoch, AND the still-displayed ref's - the press's whole capture pair in
  // ONE readonly snapshot, so the press opens one transaction instead of two
  // and both fence halves read one consistent baseline. The Send press
  // requests it in its own synchronous prefix because the row may be
  // retargeted by a peer while the Send waits on its persistence or on
  // destination hydration - the snapshot's destination and its epoch are the
  // fence's baseline, so a Stop pressed after the press stays in that fence's
  // future, never its baseline.
  async getRecoveryWithStopEpoch(
    clientMutationId: string,
    displayedRef?: string,
  ): Promise<{ record: MutationRecoveryRecord | undefined; stopEpoch: number; displayedStopEpoch: number }> {
    return this.#rowWithStopEpoch(RECOVERY_STORE, clientMutationId, () => displayedRef);
  }

  // Stop's durable write: the ref's cancelable rows turn "canceled" in the
  // same readwrite transaction that enqueues the turn/interrupt record, so
  // the user's click is the cancel moment and both land or neither does.
  // The cancellations are written before the interrupt is added so the scan
  // cannot cancel the interrupt itself, and an aborted commit rolls both back.
  async enqueueInterruptAndCancel(intent: MutationIntent): Promise<MutationOutboxRecord> {
    if (!intent.targetRef.trim()) throw new Error("targetRef is required");
    // Round-12: the Stop's cancel retires its canceled rows from live
    // delivery, so their ids reach the runtime's delivery registrations
    // through the same post-commit channel - a Stop-canceled row must not
    // keep the fallback guard armed on work that will never dispatch.
    const retiredIds: string[] = [];
    const interrupted = await this.#write(ENQUEUE_STORES, "enqueueInterruptAndCancel", async (transaction) => {
      const outbox = transaction.objectStore(OUTBOX_STORE);
      retiredIds.push(...(await this.#cancelUnattempted(transaction, intent.targetRef)));
      await this.#bumpStopEpoch(transaction, intent.targetRef);
      const intentSequence = await this.#allocateSequence(transaction, intent.targetRef);
      const clientMutationId = this.#createMutationId();
      await this.#assertMutationIdAvailable(transaction, clientMutationId);
      // The Stop's own interrupt passes no barrier - it IS the click the
      // epoch records - so its capture is the epoch its Stop just bumped: a
      // refusal of the interrupt itself redelivers like any transient
      // refusal instead of fence-canceling the Stop's own row.
      const stopEpoch = await this.#stopEpochOf(transaction, intent.targetRef);
      const record: MutationOutboxRecord = {
        ...intent,
        payload: { ...intent.payload, clientMutationId },
        version: 1,
        clientMutationId,
        originClientId: ownClientId(),
        intentSequence,
        createdAt: this.#now(),
        state: "submitting",
        attempted: false,
        stopEpochAtEnqueue: stopEpoch,
      };
      await requestResult(outbox.add(record));
      return record;
    });
    if (retiredIds.length > 0) this.#retiredIdsListener?.(retiredIds);
    return interrupted;
  }

  // forceStop/shutdown carry no interrupt record, but their cancellation
  // write obeys the same rule: rows canceled first, stop action second, so a
  // storage failure aborts the stop instead of orphaning it. Returns the ids
  // that moved to "canceled".
  async cancelUnattempted(targetRef: string): Promise<string[]> {
    return this.#write([OUTBOX_STORE, SEQUENCE_STORE], "cancelUnattempted", async (transaction) => {
      const canceled = await this.#cancelUnattempted(transaction, targetRef);
      await this.#bumpStopEpoch(transaction, targetRef);
      return canceled;
    });
  }

  async #cancelUnattempted(transaction: IDBTransaction, targetRef: string): Promise<string[]> {
    const store = transaction.objectStore(OUTBOX_STORE);
    const records = await requestResult<MutationOutboxRecord[]>(store.getAll());
    const canceled: string[] = [];
    for (const record of records) {
      // A retargeted recovery resend carries the displayed ref it was sent
      // from, so a Stop on that still-displayed session cancels it too - the
      // post-enqueue half of the dual-ref Stop fence. Ordinary rows name only
      // their own targetRef.
      if (record.targetRef !== targetRef && record.displayedRef !== targetRef) continue;
      if (!isCancelableByStop(record)) continue;
      await requestResult(store.put({ ...record, state: "canceled" }));
      canceled.push(record.clientMutationId);
    }
    return canceled;
  }

  // A canceled row's only exits are an explicit Retry and the thread going
  // away; this is the going-away half, called when a clear lands or the hub's
  // deletion fence proves the ref is gone. Returns the deleted ids.
  async discardCanceled(targetRef: string): Promise<string[]> {
    return this.#write(OUTBOX_STORE, "discardCanceled", async (transaction) => {
      const store = transaction.objectStore(OUTBOX_STORE);
      const records = await requestResult<MutationOutboxRecord[]>(store.getAll());
      const discarded: string[] = [];
      for (const record of records) {
        if (record.targetRef !== targetRef || record.state !== "canceled") continue;
        await requestResult(store.delete(record.clientMutationId));
        discarded.push(record.clientMutationId);
      }
      return discarded;
    });
  }

  // §6's cross-tab removal: clear in one tab, observe in another. A published
  // replacement instance proves the superseded instance is gone, so its
  // canceled rows leave in whichever tab observes the transition. Rows of any
  // other instance - including the current one, whose explicit Retry is still
  // the user's to make - are never touched.
  async discardCanceledOfInstance(targetRef: string, supersededInstanceId: string): Promise<string[]> {
    return this.#write(OUTBOX_STORE, "discardCanceled", async (transaction) => {
      const store = transaction.objectStore(OUTBOX_STORE);
      const records = await requestResult<MutationOutboxRecord[]>(store.getAll());
      const discarded: string[] = [];
      for (const record of records) {
        if (record.targetRef !== targetRef || record.state !== "canceled") continue;
        // The fused identity the fence uses (instanceId ?? threadId), never
        // threadId alone: a replacement can rotate the instance while
        // retaining the thread id, and the rows such a replacement obsoletes
        // carry the enqueue-time instance this must compare. Rows written
        // before the field existed fall back to their threadId — exactly
        // what a model with no instanceId presents.
        if ((record.instanceId ?? record.threadId) !== supersededInstanceId) continue;
        await requestResult(store.delete(record.clientMutationId));
        discarded.push(record.clientMutationId);
      }
      return discarded;
    });
  }

  // The one re-queue of a parked row: an explicit user Retry, as a MINT. The
  // old row is never reopened, refreshed, or re-validated - it settles
  // terminal, and the work moves to a FRESH row through the enqueue shape,
  // fenced at birth in this same transaction. The capture is the click's own
  // snapshot (getOutboxWithStopEpoch, the Retry twin of enqueueIntent's
  // barrier read), and it is the mint's one check:
  //  - Identity: the row must still be parked in the state the capture read
  //    (canceled or blockedUnknown) with the same durable destination and
  //    fused instance (instanceId ?? threadId). A peer retarget inside the
  //    capture-to-mint window changes the row's world out from under the
  //    press, and the mint refuses - the park record's survivor, dead by
  //    construction here: no stored past travels with the mint, so no
  //    release transaction can compare a captured epoch against a
  //    possibly-retargeted row.
  //  - Birth fence: a Stop whose cancel transaction committed between the
  //    capture and this mint bumped an epoch past its capture half. The mint
  //    never absorbs it: the fresh row commits born-"canceled" for it -
  //    announced for the canceled strip, never dispatched - the same
  //    commit-order comparison the enqueue barrier carries. A capture taken
  //    after the newest Stop compares equal and mints born-submitting: that
  //    Retry is §4's deliberate post-Stop send.
  // The terminal settlement differs by predecessor: a canceled row is
  // REMOVED (§6's removal on explicit Retry - it provably never left any
  // tab, holds no evidence a receipt could settle, and its chip must leave
  // the canceled strip so a second press cannot mint the same send twice);
  // a blockedUnknown row keeps its id and attempt evidence as "superseded"
  // - never dispatched again (nextDispatchable returns only submitting
  // rows), still settleable by the receipt or in-band refusal of the real
  // in-flight attempt exactly as before. The mint creates sibling work; it
  // never claims to replace the predecessor's outcome. The wire id follows
  // the predecessor: a canceled row provably never left any tab, so the
  // fresh row carries its own fresh id; a blockedUnknown row may already be
  // delivered, so the fresh row inherits the predecessor's id inside its
  // payload - the daemon journals client mutations by id and renders the
  // re-submission from that record with disposition "replayed", never
  // double-applying the send.
  async mintRetryFromRow(
    clientMutationId: string,
    capture: MutationRetryCapture,
  ): Promise<MutationOutboxRecord | undefined> {
    // Round-12: every key the mint retires - the predecessor the caller
    // settles or removes, and the bound's swept redundant siblings -
    // publishes through the post-commit retiredIds channel, so the runtime's
    // delivery registrations resolve synchronously instead of waiting for a
    // background pin refresh that failing storage can starve forever.
    const retiredIds: string[] = [];
    const minted = await this.#write(ENQUEUE_STORES, "mintRetry", async (transaction) => {
      const store = transaction.objectStore(OUTBOX_STORE);
      const record = await requestResult<MutationOutboxRecord | undefined>(store.get(clientMutationId));
      if (record === undefined) return undefined;
      if (record.state !== "canceled" && record.state !== "blockedUnknown") return undefined;
      // The one identity check: the durable world the capture vouched for is
      // the only one this press may re-queue under.
      const captured = capture.record;
      if (record.targetRef !== captured?.targetRef) return undefined;
      if ((record.instanceId ?? record.threadId) !== (captured.instanceId ?? captured.threadId)) return undefined;
      // The terminal settlement of the predecessor.
      if (record.state === "blockedUnknown") {
        await requestResult(store.put({ ...record, state: "superseded" }));
      } else {
        await requestResult(store.delete(clientMutationId));
      }
      retiredIds.push(clientMutationId);
      // The birth fence: both epochs read in this same transaction, compared
      // against the capture the way enqueueIntent compares its barrier.
      const stopEpoch = await this.#stopEpochOf(transaction, record.targetRef);
      const displayedStopEpoch =
        record.displayedRef === undefined ? undefined : await this.#stopEpochOf(transaction, record.displayedRef);
      const bornCanceled =
        stopEpoch > capture.stopEpoch ||
        (displayedStopEpoch !== undefined && displayedStopEpoch > capture.displayedStopEpoch);
      return this.#mintFreshRow(
        transaction,
        record,
        {
          stopEpoch,
          displayedStopEpoch,
          bornCanceled,
        },
        retiredIds,
      );
    });
    if (retiredIds.length > 0) this.#retiredIdsListener?.(retiredIds);
    return minted;
  }

  async listOutbox(targetRef?: string): Promise<MutationOutboxRecord[]> {
    return this.#read(OUTBOX_STORE, async (transaction) => {
      const records = await requestResult<MutationOutboxRecord[]>(transaction.objectStore(OUTBOX_STORE).getAll());
      return records
        .filter((record) => targetRef === undefined || record.targetRef === targetRef)
        .sort((left, right) => left.intentSequence - right.intentSequence);
    });
  }

  // The displayed-ref half of the persistence read: a retargeted row's
  // durable destination is the resumed ref, but the pane that surfaces it -
  // the canceled strip's projection - is the displayed one it was
  // retargeted from. This read lists the outbox rows whose displayedRef
  // names the given ref, whatever their destination; rows whose own
  // targetRef is the ref are listOutbox's, so callers merge without
  // duplicating them.
  async listOutboxDisplayedAt(targetRef: string): Promise<MutationOutboxRecord[]> {
    return this.#read(OUTBOX_STORE, async (transaction) => {
      const records = await requestResult<MutationOutboxRecord[]>(transaction.objectStore(OUTBOX_STORE).getAll());
      return records
        .filter((record) => record.displayedRef === targetRef)
        .sort((left, right) => left.intentSequence - right.intentSequence);
    });
  }

  async listTargetRefs(): Promise<string[]> {
    const [outbox, optimistic] = await Promise.all([this.listOutbox(), this.listOptimistic()]);
    return [...new Set([...outbox, ...optimistic].map((record) => record.targetRef))].sort();
  }

  // R09: a store-driven resume can return a different identity, and the rows
  // parked under the superseded ref can never dispatch again (nothing names that
  // ref), so they follow the pane to the resumed ref. targetRef and payload.ref
  // are rewritten, the identity fields (threadId, instanceId, and the payload's
  // expectedInstanceId) are rewritten to the resumed identity so the daemon's
  // instance fence accepts the row, and a fresh sequence is allocated under the
  // new ref (keeping the relative order). The clientMutationId is PRESERVED: the
  // resumed hydration's reconciliation and every settle lookup key on it. The
  // matching optimistic copies and the recovery rows (a rejected turn awaiting
  // Retry) move too, so the new ref's queue strip and recovery list show them
  // and Retry's fused-identity check (retryBlockedMutation) matches the resumed
  // model. Canceled rows stay behind - a Stop canceled them, and only an
  // explicit Retry on the identity that owns them revives one. Callers
  // re-target only while the old ref's dispatch is held closed by the recovery
  // obligation, so no row here is mid-dispatch.
  async retargetOutbox(
    fromRef: string,
    toRef: string,
    identity: { threadId?: string; instanceId?: string },
  ): Promise<RetargetedMutations> {
    if (fromRef === toRef) return { outbox: [], optimistic: [], recovery: [] };
    return this.#write(
      [OUTBOX_STORE, OPTIMISTIC_STORE, RECOVERY_STORE, SEQUENCE_STORE],
      "retargetOutbox",
      async (transaction) => {
        const outboxStore = transaction.objectStore(OUTBOX_STORE);
        const optimisticStore = transaction.objectStore(OPTIMISTIC_STORE);
        const recoveryStore = transaction.objectStore(RECOVERY_STORE);
        const sequenceStore = transaction.objectStore(SEQUENCE_STORE);
        // One read of the new ref's sequence row, then consecutive numbering for
        // the whole batch, then one put - the lists are sorted, so order is kept.
        const sequence = await requestResult<TargetSequence | undefined>(sequenceStore.get(toRef));
        let lastSequence = sequence?.lastSequence ?? 0;
        // The refusal fence compares against the destination ref's own
        // counter, so a moved row's capture rebases to that counter here -
        // the source ref's historical epoch must not travel with the row.
        const toStopEpoch = await this.#stopEpochOf(transaction, toRef);
        // The identity token the daemon's instance fence compares: the same
        // fused reading a fresh send takes (instanceId ?? threadId).
        const instanceToken = identity.instanceId ?? identity.threadId;
        // The single rewrite every loop shares: a fresh sequence, the new target
        // ref, payload.ref, and the resumed identity's threadId/instanceId and
        // expectedInstanceId. The clientMutationId is deliberately untouched.
        const move = async (store: IDBObjectStore, record: MutationRecord): Promise<MutationRecord> => {
          lastSequence += 1;
          const retargeted: MutationRecord = {
            ...record,
            targetRef: toRef,
            threadId: identity.threadId,
            instanceId: identity.instanceId,
            payload: {
              ...record.payload,
              ref: toRef,
              ...("expectedInstanceId" in record.payload ? { expectedInstanceId: instanceToken } : {}),
            },
            intentSequence: lastSequence,
            ...("stopEpochAtEnqueue" in record ? { stopEpochAtEnqueue: toStopEpoch } : {}),
          };
          await requestResult(store.put(retargeted));
          return retargeted;
        };
        const bySequence = <T extends { intentSequence: number }>(records: T[]) =>
          records.sort((left, right) => left.intentSequence - right.intentSequence);
        const eligibleOutbox = bySequence(
          (await requestResult<MutationOutboxRecord[]>(outboxStore.getAll())).filter(
            (record) => record.targetRef === fromRef && record.state !== "canceled",
          ),
        );
        // The accepted-but-unreflected copies follow whether or not their outbox
        // row is still present: the resumed ref's reconciliation settles them by
        // clientMutationId, and the new ref's strip renders them.
        const eligibleOptimistic = bySequence(
          (await requestResult<MutationOptimisticRecord[]>(optimisticStore.getAll())).filter(
            (record) => record.targetRef === fromRef,
          ),
        );
        // A recovery row is a rejected turn awaiting Retry; Retry's own
        // fused-identity check compares the row's instanceId ?? threadId against
        // the press model's, so under the superseded ref it can never be retried
        // and the identity rewrite below is what makes it reachable under the
        // resumed one.
        const eligibleRecovery = bySequence(
          (await requestResult<MutationRecoveryRecord[]>(recoveryStore.getAll())).filter(
            (record) => record.targetRef === fromRef,
          ),
        );
        const outbox: MutationOutboxRecord[] = [];
        const optimistic: MutationOptimisticRecord[] = [];
        const recovery: MutationRecoveryRecord[] = [];
        for (const record of eligibleOutbox) outbox.push((await move(outboxStore, record)) as MutationOutboxRecord);
        for (const record of eligibleOptimistic)
          optimistic.push((await move(optimisticStore, record)) as MutationOptimisticRecord);
        for (const record of eligibleRecovery)
          recovery.push((await move(recoveryStore, record)) as MutationRecoveryRecord);
        if (lastSequence !== (sequence?.lastSequence ?? 0)) {
          await requestResult(sequenceStore.put({ ...sequence, targetRef: toRef, lastSequence }));
        }
        return { outbox, optimistic, recovery };
      },
    );
  }

  async getOptimistic(clientMutationId: string): Promise<MutationOptimisticRecord | undefined> {
    return this.#read(OPTIMISTIC_STORE, async (transaction) => {
      return requestResult<MutationOptimisticRecord | undefined>(
        transaction.objectStore(OPTIMISTIC_STORE).get(clientMutationId),
      );
    });
  }

  async listOptimistic(targetRef?: string): Promise<MutationOptimisticRecord[]> {
    return this.#read(OPTIMISTIC_STORE, async (transaction) => {
      const records = await requestResult<MutationOptimisticRecord[]>(
        transaction.objectStore(OPTIMISTIC_STORE).getAll(),
      );
      return records
        .filter((record) => targetRef === undefined || record.targetRef === targetRef)
        .sort((left, right) => left.intentSequence - right.intentSequence);
    });
  }

  async settleReceipt(clientMutationId: string, projectionState: string): Promise<boolean> {
    return (await this.settleReceiptResult(clientMutationId, projectionState)).settled;
  }

  // The settlement's full outcome: what settled and every sibling row the
  // wire sweep retired, so the runtime can resolve in-memory bookkeeping
  // synchronously (the boolean settleReceipt is what the shared dispatcher
  // interface exposes; retiredIds is this host's own bookkeeping channel).
  async settleReceiptResult(
    clientMutationId: string,
    projectionState: string,
  ): Promise<{ settled: boolean; retiredIds: string[] }> {
    let settledSource: MutationRecord | undefined;
    let settledWireId: string | undefined;
    const settled = await this.#write<{ settled: boolean; retiredIds: string[] }>(
      [OUTBOX_STORE, OPTIMISTIC_STORE, RECOVERY_STORE],
      "settleReceipt",
      async (transaction) => {
        const outbox = transaction.objectStore(OUTBOX_STORE);
        const optimistic = transaction.objectStore(OPTIMISTIC_STORE);
        const recovery = transaction.objectStore(RECOVERY_STORE);
        const [outboxRecord, optimisticRecord, recoveryRecord] = await Promise.all([
          requestResult<MutationOutboxRecord | undefined>(outbox.get(clientMutationId)),
          requestResult<MutationOptimisticRecord | undefined>(optimistic.get(clientMutationId)),
          requestResult<MutationRecoveryRecord | undefined>(recovery.get(clientMutationId)),
        ]);
        const named = outboxRecord ?? recoveryRecord ?? optimisticRecord;
        // The wire id the settlement travels under: the daemon journals the
        // composition by it, so a receipt proves EVERY record sharing it -
        // the superseded predecessor behind a settled replacement, or the
        // replacement carrying a predecessor's id when the feed names the
        // predecessor's own record. Resolve the pair by effective wire
        // identity, or one side lingers retained and pinning its ref after
        // delivery.
        const wireId = named ? effectiveWireId(named) : clientMutationId;
        // The main settle write reads only the outbox and optimistic stores:
        // a submission's settlement must never WAIT on recovery projection
        // reads (the composer's committed-send contract, the same shape the
        // resend's consume sweep pins). The wire's unedited recovery siblings
        // retire in the sweep the settle initiates on its way out, below.
        const family = await this.#resolveWireFamily(transaction, wireId, {
          readOptimistic: true,
          readRecovery: false,
        });
        settledWireId = wireId;
        const siblings = [...family.outbox, ...family.optimistic].filter(
          (record) => record.clientMutationId !== clientMutationId,
        );
        // Round-6 M:776: a wire whose every outbox and optimistic carrier is
        // gone but whose unedited recovery copy still lives has a subject to
        // settle - the late authoritative event proves the WIRE delivered,
        // and the sendable tray row claiming already-delivered work must
        // retire. This recovery read happens only when nothing else named
        // the wire, so the ordinary settlement never waits on it. An edited
        // composition is its own work and survives (settlementRetiresRecovery).
        let wireCarriers: MutationRecoveryRecord[] = [];
        if (!named && siblings.length === 0) {
          const recoveryFamily = await this.#resolveWireFamily(transaction, wireId, { readRecovery: true });
          wireCarriers = recoveryFamily.recovery.filter(
            (record) => record.clientMutationId !== clientMutationId && settlementRetiresRecovery(record),
          );
          if (wireCarriers.length === 0) return { settled: false, retiredIds: [] };
        }
        // The named record settles with today's semantics; when the receipt
        // names a wire id whose own record is already gone, the replacement
        // carrying that wire id is the subject. Every other sibling only
        // retires - the composition settled once, and one accepted copy is
        // enough.
        const subject = (named ?? siblings[0] ?? wireCarriers[0]) as MutationRecord;
        settledSource = subject;
        await this.#discardSupersededNoteRecovery(transaction, subject);
        const supersededNoteIds = await this.#retireOlderBlockedNoteRows(transaction, subject);
        const retained = retainsAcceptedCopy(subject, projectionState);
        if (retained) {
          // The accepted whitelist lives in the package's acceptedRecord so
          // this adapter and native's build the same row.
          await requestResult(optimistic.put(acceptedRecord(subject)));
        } else if (optimisticRecord) {
          // The delete stays CONDITIONAL on the named optimistic record: a
          // repeated pending receipt must leave the retained accepted copy
          // in place (the round-1 M3 corner - an unconditional delete of
          // whatever the wire pair holds would erase accepted input's only
          // durable pending representation).
          await requestResult(optimistic.delete(clientMutationId));
        }
        if (named) {
          if (outboxRecord) await requestResult(outbox.delete(clientMutationId));
          if (recoveryRecord && settlementRetiresRecovery(recoveryRecord))
            await requestResult(recovery.delete(clientMutationId));
        } else if (wireCarriers.length > 0) {
          // The wire lives only as unedited recovery copies: the carrier is
          // the subject and retires with the settlement.
          await requestResult(recovery.delete(subject.clientMutationId));
        } else {
          // A receipt for a record already gone settles the replacement
          // that carries its wire id.
          await requestResult(outbox.delete(subject.clientMutationId));
          if (!retained) await requestResult(optimistic.delete(subject.clientMutationId));
        }
        // The one sibling sweep: the wire family's outbox and optimistic rows
        // here; an unedited recovery copy of the wire retires with the
        // settlement too (it is sendable delivered work otherwise) in the
        // sweep initiated below, while an independently edited composition
        // survives every sweep.
        const retiredIds = await this.#retireWireFamily(transaction, family, {
          excludeKeys: new Set([clientMutationId, subject.clientMutationId]),
          sweepOptimistic: true,
          sweepRecovery: false,
        });
        // Round-6 M:817: a fallback subject (the replacement the event
        // settled by wire identity, or a carrier above) was deleted from
        // storage by this settlement, so its key reaches the retired ids the
        // runtime clears in-memory registrations from - exactly like the
        // sweep's own rows. The named record's id the runtime clears itself.
        if (named === undefined) retiredIds.push(subject.clientMutationId);
        retiredIds.push(...supersededNoteIds);
        return { settled: true, retiredIds };
      },
    );
    if (!settled.settled) return settled;
    // The settlement's own write never waits on recovery projection reads;
    // this sweep - initiated here, fire-and-forget like the supersede
    // discard - retires the wire's unedited recovery siblings through the
    // same one-home rules. A later read of the recovery store queues behind
    // it (same-connection transaction ordering), so observers see the
    // retired state, never the stale sendable one.
    if (settledWireId !== undefined) {
      this.#sweepSettledWireRecoverySiblings(settledWireId, [
        clientMutationId,
        settledSource?.clientMutationId ?? clientMutationId,
      ]);
    }
    this.#discardSupersededCanceledNotesAfterSettle(settledSource);
    const settledRef = settledSource?.targetRef;
    if (settledRef !== undefined) this.#notifyQuietly(() => this.#settledListener?.(settledRef));
    if (settled.retiredIds.length > 0) this.#retiredIdsListener?.(settled.retiredIds);
    return settled;
  }

  async settleApplied(clientMutationId: string): Promise<MutationRecord | undefined> {
    return (await this.settleAppliedResult(clientMutationId)).record;
  }

  async settleAppliedResult(
    clientMutationId: string,
  ): Promise<{ settled: boolean; retiredIds: string[]; record?: MutationRecord }> {
    let settledSource: MutationRecord | undefined;
    let settledWireId: string | undefined;
    const settled = await this.#write<{ settled: boolean; retiredIds: string[] }>(
      [OUTBOX_STORE, OPTIMISTIC_STORE, RECOVERY_STORE],
      undefined,
      async (transaction) => {
        const outbox = transaction.objectStore(OUTBOX_STORE);
        const optimistic = transaction.objectStore(OPTIMISTIC_STORE);
        const recovery = transaction.objectStore(RECOVERY_STORE);
        const [outboxRecord, optimisticRecord, recoveryRecord] = await Promise.all([
          requestResult<MutationOutboxRecord | undefined>(outbox.get(clientMutationId)),
          requestResult<MutationOptimisticRecord | undefined>(optimistic.get(clientMutationId)),
          requestResult<MutationRecoveryRecord | undefined>(recovery.get(clientMutationId)),
        ]);
        const named = outboxRecord ?? optimisticRecord ?? recoveryRecord;
        // The same wire-identity resolution as settleReceipt: an applied
        // settlement proves the whole wire pair, and retiring the other side
        // is what keeps a superseded predecessor from lingering retained
        // and pinned after its replacement is applied.
        const wireId = named ? effectiveWireId(named) : clientMutationId;
        // The main settle write reads only the outbox and optimistic stores:
        // a submission's settlement must never WAIT on recovery projection
        // reads (the composer's committed-send contract, the same shape the
        // resend's consume sweep pins). The wire's unedited recovery siblings
        // retire in the sweep the settle initiates on its way out, below.
        const family = await this.#resolveWireFamily(transaction, wireId, {
          readOptimistic: true,
          readRecovery: false,
        });
        settledWireId = wireId;
        const siblings = [...family.outbox, ...family.optimistic].filter(
          (record) => record.clientMutationId !== clientMutationId,
        );
        // Round-6 M:776: a wire whose every outbox and optimistic carrier is
        // gone but whose unedited recovery copy still lives has a subject to
        // settle - the late authoritative event proves the WIRE delivered,
        // and the sendable tray row claiming already-delivered work must
        // retire. This recovery read happens only when nothing else named
        // the wire, so the ordinary settlement never waits on it. An edited
        // composition is its own work and survives (settlementRetiresRecovery).
        let wireCarriers: MutationRecoveryRecord[] = [];
        if (!named && siblings.length === 0) {
          const recoveryFamily = await this.#resolveWireFamily(transaction, wireId, { readRecovery: true });
          wireCarriers = recoveryFamily.recovery.filter(
            (record) => record.clientMutationId !== clientMutationId && settlementRetiresRecovery(record),
          );
          if (wireCarriers.length === 0) return { settled: false, retiredIds: [], record: undefined };
        }
        const subject = (named ?? siblings[0] ?? wireCarriers[0]) as MutationRecord;
        await this.#discardSupersededNoteRecovery(transaction, subject);
        const supersededNoteIds = await this.#retireOlderBlockedNoteRows(transaction, subject);
        if (named) {
          if (outboxRecord) await requestResult(outbox.delete(clientMutationId));
          if (optimisticRecord) await requestResult(optimistic.delete(clientMutationId));
          if (recoveryRecord && settlementRetiresRecovery(recoveryRecord))
            await requestResult(recovery.delete(clientMutationId));
        } else if (wireCarriers.length > 0) {
          // The wire lives only as unedited recovery copies: the carrier is
          // the subject and retires with the settlement.
          await requestResult(recovery.delete(subject.clientMutationId));
        } else {
          await requestResult(outbox.delete(subject.clientMutationId));
          await requestResult(optimistic.delete(subject.clientMutationId));
        }
        // The same one sibling sweep as settleReceipt.
        const retiredIds = await this.#retireWireFamily(transaction, family, {
          excludeKeys: new Set([clientMutationId, subject.clientMutationId]),
          sweepOptimistic: true,
          sweepRecovery: false,
        });
        // The same M:817 rule as settleReceipt: a fallback subject's key
        // reaches the retired ids.
        if (named === undefined) retiredIds.push(subject.clientMutationId);
        retiredIds.push(...supersededNoteIds);
        settledSource = subject;
        return { settled: true, retiredIds, record: subject };
      },
    );
    if (!settled.settled) return settled;
    // The settlement's own write never waits on recovery projection reads;
    // this sweep - initiated here, fire-and-forget like the supersede
    // discard - retires the wire's unedited recovery siblings through the
    // same one-home rules. A later read of the recovery store queues behind
    // it (same-connection transaction ordering), so observers see the
    // retired state, never the stale sendable one.
    if (settledWireId !== undefined) {
      this.#sweepSettledWireRecoverySiblings(settledWireId, [
        clientMutationId,
        settledSource?.clientMutationId ?? clientMutationId,
      ]);
    }
    this.#discardSupersededCanceledNotesAfterSettle(settledSource);
    const settledRef = settledSource?.targetRef;
    if (settledRef !== undefined) this.#notifyQuietly(() => this.#settledListener?.(settledRef));
    if (settled.retiredIds.length > 0) this.#retiredIdsListener?.(settled.retiredIds);
    return settled;
  }

  // THE WIRE FAMILY, one home for "which records belong to this composition":
  // every record of one effective wire id across all three stores. The wire
  // is the identity the daemon journals and settles prove, so every sweep -
  // a settlement retiring the wire's remaining rows, the recovery resend
  // consuming a wire, the transfer coalesce looking for a live copy - must ask
  // the SAME question or the sweeps diverge per seam, which is exactly the
  // corner pattern rounds 3-5 kept finding (a submitting sibling here, a
  // recovery sibling there, one store forgotten).
  async #resolveWireFamily(
    transaction: IDBTransaction,
    wireId: string,
    options: { readOptimistic?: boolean; readRecovery?: boolean } = {},
  ): Promise<{
    outbox: MutationOutboxRecord[];
    optimistic: MutationOptimisticRecord[];
    recovery: MutationRecoveryRecord[];
  }> {
    // Reads only the stores this transaction spans AND the caller sweeps: a
    // settlement resolves all three once; the resend consume reads the
    // outbox alone (its write must not wait on recovery projection reads);
    // the transfer coalesce asks for the recovery copies only. One read
    // round per question - the note editor's tightly-sequenced flows wedge
    // on extra read rounds inside a write (the markAttempted precedent), so
    // no sweeper re-resolves what it already holds.
    const read = async <T>(store: string, wanted: boolean): Promise<T[]> =>
      wanted && transaction.objectStoreNames.contains(store)
        ? requestResult<T[]>(transaction.objectStore(store).getAll())
        : [];
    const [outbox, optimistic, recovery] = await Promise.all([
      read<MutationOutboxRecord>(OUTBOX_STORE, true),
      read<MutationOptimisticRecord>(OPTIMISTIC_STORE, options.readOptimistic === true),
      read<MutationRecoveryRecord>(RECOVERY_STORE, options.readRecovery === true),
    ]);
    return {
      outbox: outbox.filter((record) => effectiveWireId(record) === wireId),
      optimistic: optimistic.filter((record) => effectiveWireId(record) === wireId),
      recovery: recovery.filter((record) => effectiveWireId(record) === wireId),
    };
  }

  // Retire the wire's records, one rule per store so every caller shares it:
  // outbox rows delete whatever their state (the wire's outcome decided, they
  // are dead work; their late outcomes land inert on a missing row, the same
  // semantics a late receipt against a settled row already has); recovery rows
  // retire only when UNEDITED (settlementRetiresRecovery - an independently
  // edited composition survives every sweep); optimistic rows delete only when
  // the caller proves delivery (a settlement) - a resend has no such proof and
  // leaves accepted copies to settlement's own authority. Returns every retired
  // storage key so the caller can resolve in-memory bookkeeping synchronously.
  // The family is one the caller already resolved (#resolveWireFamily), so
  // no sweeper re-reads what it holds.
  async #retireWireFamily(
    transaction: IDBTransaction,
    family: {
      outbox: MutationOutboxRecord[];
      optimistic: MutationOptimisticRecord[];
      recovery: MutationRecoveryRecord[];
    },
    options: {
      excludeKeys: ReadonlySet<string>;
      sweepOptimistic: boolean;
      sweepRecovery: boolean;
      // The generation bound's state filter: retire only terminal
      // "superseded" copies, leaving every LIVE row of the wire untouched.
      // Used by the mint (#mintFreshRow) - the redundant older generations
      // of a wire are safe to delete precisely because they are terminal,
      // while a submitting sibling mid-flight keeps its lifecycle and
      // evidence whatever the mint is doing.
      onlySuperseded?: boolean;
    },
  ): Promise<string[]> {
    const retired: string[] = [];
    for (const record of family.outbox) {
      if (options.excludeKeys.has(record.clientMutationId)) continue;
      if (options.onlySuperseded === true && record.state !== "superseded") continue;
      await requestResult(transaction.objectStore(OUTBOX_STORE).delete(record.clientMutationId));
      retired.push(record.clientMutationId);
    }
    if (options.sweepOptimistic) {
      for (const record of family.optimistic) {
        if (options.excludeKeys.has(record.clientMutationId)) continue;
        await requestResult(transaction.objectStore(OPTIMISTIC_STORE).delete(record.clientMutationId));
        retired.push(record.clientMutationId);
      }
    }
    if (options.sweepRecovery) {
      for (const record of family.recovery) {
        if (options.excludeKeys.has(record.clientMutationId)) continue;
        if (!settlementRetiresRecovery(record)) continue;
        await requestResult(transaction.objectStore(RECOVERY_STORE).delete(record.clientMutationId));
        retired.push(record.clientMutationId);
      }
    }
    return retired;
  }

  // A settlement's recovery-sibling tail: the settle's own write never reads
  // the recovery store (a submission's settlement must not wait on recovery
  // projection reads), so the unedited same-wire recovery copies - sendable
  // delivered work once the wire settles - retire here, initiated with the
  // settle and fire-and-forget like the supersede discard. An independently
  // edited composition survives it (settlementRetiresRecovery), and the
  // retired keys reach the host's bookkeeping through the same listener.
  #sweepSettledWireRecoverySiblings(wireId: string, exclude: string[]): void {
    void this.#write([RECOVERY_STORE], undefined, async (transaction) => {
      const family = await this.#resolveWireFamily(transaction, wireId, { readRecovery: true });
      const retiredIds = await this.#retireWireFamily(transaction, family, {
        excludeKeys: new Set(exclude),
        sweepOptimistic: false,
        sweepRecovery: true,
      });
      if (retiredIds.length > 0) this.#retiredIdsListener?.(retiredIds);
      return retiredIds;
    }).catch(() => {
      // The supersede-discard rule: a failed cleanup leaves the copies for a
      // later settlement or sweep to retire.
    });
  }

  // Resolve-and-retire for callers that do not already hold the family (the
  // resend consume). The read flags follow the sweep flags so the write never
  // waits on stores it will not touch.
  async #retireWireSiblings(
    transaction: IDBTransaction,
    wireId: string,
    options: {
      excludeKeys: ReadonlySet<string>;
      sweepOptimistic: boolean;
      sweepRecovery: boolean;
      onlySuperseded?: boolean;
    },
  ): Promise<string[]> {
    const family = await this.#resolveWireFamily(transaction, wireId, {
      readOptimistic: options.sweepOptimistic,
      readRecovery: options.sweepRecovery,
    });
    return this.#retireWireFamily(transaction, family, options);
  }

  // The settle-time retirement ruling: a settled note save is the note's
  // truth, and an OLDER blockedUnknown note row on the same target is stale
  // work that a later reconcile could still re-queue as a fresh row at the
  // ref's tail - dispatching the older text AFTER the newer save landed. The
  // mint's own ordering guard only sees newer saves that are still queued or
  // waiting; a newer save that already settled is invisible to it. Retire the
  // older row to terminal "superseded" here, in the settlement's own write:
  // state change only, keeping its id and attempt evidence so a receipt or
  // in-band refusal from its own real in-flight attempt still settles it,
  // exactly like the mint's superseded semantics - and superseded rows never
  // dispatch again (nextDispatchable returns only submitting rows).
  async #retireOlderBlockedNoteRows(
    transaction: IDBTransaction,
    source: MutationRecord | undefined,
  ): Promise<string[]> {
    if (source?.method !== "notes/human/set") return [];
    const store = transaction.objectStore(OUTBOX_STORE);
    const records = await requestResult<MutationOutboxRecord[]>(store.getAll());
    const supersededIds: string[] = [];
    for (const record of records) {
      if (
        record.method === "notes/human/set" &&
        record.targetRef === source.targetRef &&
        record.intentSequence < source.intentSequence &&
        record.state === "blockedUnknown"
      ) {
        await requestResult(store.put({ ...record, state: "superseded" }));
        supersededIds.push(record.clientMutationId);
      }
    }
    // The ids return to the settlement's retiredIds notification: these rows
    // leave every pending surface in this same write, so the runtime's
    // in-memory delivery registrations must clear through the same
    // post-commit channel every other retired row uses - or a storage wedge
    // before the next background read leaves them refusing fallback sends
    // for work that can never dispatch again (round-8 Medium).
    return supersededIds;
  }

  // A later accepted note supersedes refused earlier text, not chat recovery
  // or unresolved transport. Retire it in the same canonical-settlement write.
  async #discardSupersededNoteRecovery(transaction: IDBTransaction, source: MutationRecord | undefined): Promise<void> {
    if (source?.method !== "notes/human/set") return;
    const store = transaction.objectStore(RECOVERY_STORE);
    const records = await requestResult<MutationRecoveryRecord[]>(store.getAll());
    for (const record of records) {
      if (
        record.method === "notes/human/set" &&
        record.targetRef === source.targetRef &&
        record.intentSequence < source.intentSequence &&
        settlementRetiresRecovery(record)
      ) {
        await requestResult(store.delete(record.clientMutationId));
      }
    }
  }

  // The same supersede, for the ref's earlier stop-canceled note rows: a
  // canceled row provably never left the client, and the note editor's only
  // "retry" is the user's next save (its retry branch is gated on
  // blockedUnknown), so without this discard the canceled row - still holding
  // its note text - would pin listTargetRefs until the thread is cleared or
  // deleted. Only canceled rows leave: a newer save supersedes nothing that is
  // delivery-uncertain.
  // The supersede discard runs in its own write AFTER the settlement
  // commits, never inside it: the settle's commit boundary is the moment the
  // world (and the note editor's parked-save replay) observes the settle, and
  // carrying this scan inside the transaction delayed that boundary past what
  // the replay's staging tolerates (NotesPanel's B-save-behind-A tests).
  // Supersede cleanup is best-effort: a failed write leaves the canceled rows
  // for the next settle, clear, or delete - the discardCanceledMutations rule.
  #discardSupersededCanceledNotesAfterSettle(source: MutationRecord | undefined): void {
    if (source?.method !== "notes/human/set") return;
    void this.#write(OUTBOX_STORE, "discardCanceled", async (transaction) => {
      const store = transaction.objectStore(OUTBOX_STORE);
      const records = await requestResult<MutationOutboxRecord[]>(store.getAll());
      for (const record of records) {
        if (
          record.method === "notes/human/set" &&
          record.targetRef === source.targetRef &&
          record.state === "canceled" &&
          record.intentSequence < source.intentSequence
        ) {
          await requestResult(store.delete(record.clientMutationId));
        }
      }
    })
      .then(() => {
        // The cleanup completed, zero deletions included: the owning
        // runtime's projections and pins last saw this ref before the
        // removal, and this fire-and-forget write is the only thing that can
        // tell them.
        this.#notifyQuietly(() => this.#supersededDiscardListener?.(source.targetRef));
      })
      .catch(() => {
        // Left for the next settle, clear, or delete.
      });
  }

  // Commit attempt evidence before transport so another tab or a reload cannot
  // mistake a possibly delivered mutation for an unsent intent. Each attempt
  // counts: two tabs may dispatch one row concurrently, and the count is what
  // lets a refusal roll back its own attempt without erasing the other's. A
  // record written before the count field existed carries its evidence as
  // the bare attempted boolean, which reads as one unresolved attempt.
  async markAttempted(clientMutationId: string): Promise<boolean> {
    return this.#write([OUTBOX_STORE, SEQUENCE_STORE], "markAttempted", async (transaction) => {
      const store = transaction.objectStore(OUTBOX_STORE);
      const record = await requestResult<MutationOutboxRecord | undefined>(store.get(clientMutationId));
      if (record?.state !== "submitting") return false;
      const unresolvedAttempts = priorUnresolvedAttempts(record) + 1;
      // The choke-point fence: the ONE gate every dispatch passes, so no path
      // can put a crossed row on the wire. Every other fence (the cancel scan,
      // the born-canceled birth compare of an enqueue or a mint) is
      // defense-in-depth from here on - this comparison is the enforcement
      // point, commit-ordered against every Stop that can still lose a race
      // with the transport. A row's baselines are immutable for its whole
      // life, so the comparison is always against the row's own birth.
      //
      // The fence reads the epochs only for rows the cancel scan cannot
      // cancel. A fresh modern row (no attempt evidence, baseline stamped at
      // birth) with a lagging baseline was necessarily crossed by a Stop
      // whose own cancel scan matched it on either ref it crosses and
      // canceled it while unattempted - so by the time this transaction reads
      // the row, the state check above has already refused it. For those rows
      // the fence reads nothing and this transaction keeps the request
      // sequence byte-identical the note editor's tightly-sequenced drain
      // stages against (the extra read wedges that drain's replay - pinned by
      // NotesPanel's superseded-save test). Rows that DO read: one carrying
      // attempt evidence (a preserved attempt, or an unresolved concurrent
      // attempt - the rows the scan skips); one whose explicit count a
      // refusal's rollback wrote (a rolled-back row is attempted-then-proven-
      // unsent, and the scan argument no longer covers it - the Stop crossed
      // it while its attempt was in flight and the rollback re-opened it, so
      // this gate is the only fence left); and a row from before the capture
      // field existed. The batched read costs one extra request with nothing
      // else pending: the row's ref is unknown before the row read, so it
      // reads the whole small sequence store in the same event delivery.
      const evidenced = record.attempted === true || priorUnresolvedAttempts(record) > 0;
      const rolledBack = record.unresolvedAttempts !== undefined;
      const legacyCapture = record.stopEpochAtEnqueue === undefined;
      // Survival proves a missing capture's birth epoch only for a row with
      // NO attempt evidence: a provably unattempted row - attempted === false
      // with no unresolved-attempt count - that lived through every Stop's
      // cancel scan was necessarily never crossed, because any crossing
      // would have canceled it while unattempted. An EVIDENCED row
      // (attempted, or a live unresolved count) survived because the scan
      // SKIPS attempted rows, not because no Stop crossed, and a row from
      // before the attempted field existed is unknown evidence of the same
      // kind. Those rows keep the conservative ZERO baseline instead of the
      // current-epoch initialization: initializing to now would absorb every
      // historical Stop and let this gate redispatch input a Stop already
      // cancelled, while zero trips the fence on any historical Stop and
      // parks them delivery-uncertain, never redispatched. For the provably
      // unattempted row the epoch as it stands IS the epoch it was born at:
      // initialize the capture here, one-time, and the fence compares
      // against the birth epoch instead of an inherited zero. A row's stored
      // capture never moves again after this write.
      const provablyUnattempted = record.attempted === false && record.unresolvedAttempts === undefined;
      const uncertainEvidence = evidenced || (legacyCapture && !provablyUnattempted);
      if (evidenced || rolledBack || legacyCapture) {
        const sequences = await requestResult<TargetSequence[]>(transaction.objectStore(SEQUENCE_STORE).getAll());
        const epochOf = (ref: string) => sequences.find((sequence) => sequence.targetRef === ref)?.stopEpoch ?? 0;
        const stopEpochAtEnqueue =
          legacyCapture && provablyUnattempted ? epochOf(record.targetRef) : (record.stopEpochAtEnqueue ?? 0);
        if (
          await this.#fenceCrossedSinceEnqueue(
            transaction,
            { ...record, stopEpochAtEnqueue },
            {
              stopEpoch: epochOf(record.targetRef),
              displayedStopEpoch: record.displayedRef === undefined ? undefined : epochOf(record.displayedRef),
            },
          )
        ) {
          // The fence's verdict is the row's truth: a row with no attempt
          // evidence provably never left any tab, so the Stop's verdict is
          // truthful cancellation; a row carrying evidence may already be in
          // flight, so it parks delivery-uncertain for a receipt or refusal
          // to settle - never canceled by absence (§5: attempted rows are
          // uncertain, not canceled). The refused pass mints no attempt
          // evidence: a fence-refused second tab's write must not inflate the
          // count a real attempt's own refusal then rolls back - the count
          // stays what the real attempts made it, or the refusal would leave
          // a phantom attempt holding a provably unsent row uncertain
          // forever. The park carries the fence's stamp: the Stop is the
          // reason this row is not delivering, and the queue must still pass
          // it (the liveness semantics live at nextDispatchable).
          await requestResult(
            store.put(
              uncertainEvidence
                ? {
                    ...record,
                    state: "blockedUnknown",
                    attempted: true,
                    unresolvedAttempts: priorUnresolvedAttempts(record),
                    stopEpochAtEnqueue,
                    stopFenced: true,
                  }
                : { ...record, state: "canceled", attempted: false, unresolvedAttempts: 0, stopEpochAtEnqueue },
            ),
          );
          return false;
        }
        await requestResult(store.put({ ...record, attempted: true, unresolvedAttempts, stopEpochAtEnqueue }));
        return true;
      }
      await requestResult(store.put({ ...record, attempted: true, unresolvedAttempts }));
      return true;
    });
  }

  // Rolls the refused attempt's pre-transport evidence back after the hub
  // authoritatively refused the attempt in band (#4342's
  // connectionBackpressure). Resolves to true only when the row returns to
  // its never-attempted dispatchable shape: with earlier or concurrent
  // unresolved attempts remaining, their evidence survives (attempted stays
  // true) and the row keeps its uncertain treatment; a row parked
  // blockedUnknown while its only attempt was in flight reopens, because the
  // refusal is authoritative proof that park's uncertainty resolves, while
  // one parked with earlier evidence stays parked.
  // The refusal's rollback, and the arithmetic of which of this row's
  // attempts the refusal proves never landed - plus the one verdict the
  // refusal itself proves: #4342's rule. A Stop whose cancel scan skipped
  // this attempted row bumped the epoch while the attempt was in flight,
  // and the in-band refusal proves the attempt never landed - so the row
  // holds canceled here, released only by an explicit Retry (a mint now),
  // never dispatchable ahead of the interrupt the Stop already queued. This
  // is the Stop's own truthful verdict applied where the refusal proves the
  // row unsent, not a re-validation of stored baselines: a row an explicit
  // Retry re-queues is a FRESH row with its own birth captures, so no
  // carve-out is needed, and the comparison is always against the row's own
  // immutable birth. With markAttempted the enforcement point, this seam's
  // fence is defense-in-depth.
  async unmarkAttempted(clientMutationId: string): Promise<boolean> {
    // The fence consult below reads the ref's stop epoch, so the sequence
    // store is in this transaction's scope alongside the outbox.
    return this.#write([OUTBOX_STORE, SEQUENCE_STORE], "unmarkAttempted", async (transaction) => {
      const store = transaction.objectStore(OUTBOX_STORE);
      const record = await requestResult<MutationOutboxRecord | undefined>(store.get(clientMutationId));
      const priorAttempts = record ? priorUnresolvedAttempts(record) : 0;
      if (record?.state === "submitting") {
        const unresolvedAttempts = Math.max(0, priorAttempts - 1);
        if (await this.#fenceCrossedSinceEnqueue(transaction, record)) {
          // #4342's rollback hold: the refusal proves only ITS OWN attempt
          // never landed. With other attempts still outstanding the row
          // stays delivery-uncertain - the survivors keep their evidence and
          // the Stop's fence stamps the park so the interrupt proceeds - so
          // only a fully drained row takes the Stop's cancellation here.
          if (unresolvedAttempts > 0) {
            await requestResult(
              store.put({
                ...record,
                state: "blockedUnknown",
                attempted: true,
                unresolvedAttempts,
                stopFenced: true,
              }),
            );
            return false;
          }
          await requestResult(store.put({ ...record, state: "canceled", attempted: false, unresolvedAttempts: 0 }));
          return false;
        }
        await requestResult(store.put({ ...record, attempted: unresolvedAttempts > 0, unresolvedAttempts }));
        return unresolvedAttempts === 0;
      }
      if (record?.state === "blockedUnknown") {
        const unresolvedAttempts = priorAttempts;
        const fenceCrossed = await this.#fenceCrossedSinceEnqueue(transaction, record);
        if (unresolvedAttempts > 1) {
          // The same #4342 hold: one refusal proves one attempt. The
          // survivors keep their evidence, and a crossed fence stamps the
          // park so the interrupt proceeds.
          await requestResult(
            store.put({
              ...record,
              unresolvedAttempts: unresolvedAttempts - 1,
              ...(fenceCrossed ? { stopFenced: true } : {}),
            }),
          );
          return false;
        }
        if (fenceCrossed) {
          await requestResult(store.put({ ...record, state: "canceled", attempted: false, unresolvedAttempts: 0 }));
          return false;
        }
        await requestResult(store.put({ ...record, state: "submitting", attempted: false, unresolvedAttempts: 0 }));
        return true;
      }
      return false;
    });
  }

  async markUnknown(
    clientMutationId: string,
    state: "blockedUnknown",
    options?: { onlyAttempted: boolean },
  ): Promise<boolean> {
    // The literal type already narrows this at compile time, but an untyped
    // caller (a JS bridge, dev tooling) could still ask for "canceled" - the
    // user's durable Stop decision that only an explicit Retry releases - and
    // this delivery-uncertainty path must never fabricate one. The native
    // adapter's guard is the same check.
    if (state !== "blockedUnknown") throw new Error('markUnknown only names "blockedUnknown"');
    return this.#write(OUTBOX_STORE, undefined, async (transaction) => {
      const store = transaction.objectStore(OUTBOX_STORE);
      const record = await requestResult<MutationOutboxRecord | undefined>(store.get(clientMutationId));
      if (!record || (options?.onlyAttempted && record.attempted === false)) return false;
      // A canceled row is the user's durable decision, and a superseded row
      // is a settled terminal fact (its work lives in the fresh row a mint
      // or reconcile queued); an uncertain-outcome write must reclassify
      // neither back into something a reconcile could re-queue.
      if (record.state === "canceled" || record.state === "superseded") return false;
      if (record.state !== state) await requestResult(store.put({ ...record, state }));
      return true;
    });
  }

  // Reopen unresolved records after a live authoritative read. Missing IDs
  // are not proof of non-delivery: bounded transcripts omit older work.
  // Preserve the original mutation ID and entire payload so the daemon's
  // durable journal can replay its receipt, or the original instance fence
  // can reject a retry after a clear. Known IDs stay on the receipt path.
  // Returns the IDs this pass decided: each reopened to submitting, or - for
  // a retargeted resend that took a Stop on either ref it crosses while it
  // sat parked and provably unattempted - held canceled outright. An
  // ATTEMPTED row the same fence stops is not decided at all: its attempt may
  // already be delivered with the response lost, and absence from a bounded
  // read proves nothing, so the crossed Stop fences redispatch only - the row
  // stays delivery-uncertain for a receipt or refusal to settle, the same
  // truth the refusal rollback's fence derives from an actual refusal.
  async restoreProvenAbsent(targetRef: string, authoritativeIds: ReadonlySet<string>): Promise<MutationOutboxRecord[]> {
    // Round-12: the automatic mint published nothing while the explicit
    // Retry wrapper transferred both registrations - so a predecessor
    // superseded by reconcile kept its in-memory delivery registration until
    // the pin refresh's background read, and failing reads kept it armed on
    // work that can never dispatch again. Every key this pass retires now
    // publishes through the post-commit retiredIds channel, and the rows it
    // re-queues return to the caller so it registers them synchronously -
    // the same transfer the explicit Retry wrapper performs at its mint.
    const retiredIds: string[] = [];
    const restored = await this.#write(ENQUEUE_STORES, undefined, async (transaction) => {
      const store = transaction.objectStore(OUTBOX_STORE);
      const records = await requestResult<MutationOutboxRecord[]>(store.getAll());
      const requeued: MutationOutboxRecord[] = [];
      // The epochs every parked row on this ref compares against, read once
      // for the pass (displayed refs memoized: retargeted rows share one).
      const targetStopEpoch = await this.#stopEpochOf(transaction, targetRef);
      const displayedStopEpochs = new Map<string, number>();
      for (const record of records) {
        if (record.targetRef !== targetRef) continue;
        if (record.state !== "blockedUnknown") continue;
        if (authoritativeIds.has(record.clientMutationId)) continue;
        // The dual-ref fence at the reconcile seam, for EVERY parked row -
        // retargeted or ordinary: a Stop that landed on either ref the send
        // crosses while the row sat parked must stop this re-queue. What the
        // fence may DECIDE depends on what the row proves: an unattempted row
        // provably never reached the hub, so the Stop's verdict is truthful
        // cancellation; an attempted row may already be delivered with its
        // response lost, and a bounded read that omits it proves nothing, so
        // the fence holds it parked, stamped with the Stop -
        // delivery-uncertain, never re-dispatched, still settleable by a
        // receipt or a refusal. With markAttempted the enforcement point,
        // this seam's fence is defense-in-depth.
        let displayedStopEpoch: number | undefined;
        if (record.displayedRef !== undefined) {
          displayedStopEpoch = displayedStopEpochs.get(record.displayedRef);
          if (displayedStopEpoch === undefined) {
            displayedStopEpoch = await this.#stopEpochOf(transaction, record.displayedRef);
            displayedStopEpochs.set(record.displayedRef, displayedStopEpoch);
          }
        }
        if (
          await this.#fenceCrossedSinceEnqueue(transaction, record, {
            stopEpoch: targetStopEpoch,
            displayedStopEpoch,
          })
        ) {
          if (record.attempted === false) {
            const fenceCanceled = { ...record, state: "canceled" as const };
            await requestResult(store.put(fenceCanceled));
            requeued.push(fenceCanceled);
            retiredIds.push(record.clientMutationId);
          } else {
            await requestResult(store.put({ ...record, state: "blockedUnknown", stopFenced: true }));
          }
          continue;
        }
        // A note save is per-session state, not FIFO work: a newer save for
        // the same note that is already queued or waiting carries the truth,
        // and a mint queues at the ref's TAIL - minting THIS older row
        // behind it would dispatch the stale note text after the newer save
        // and overwrite it. The older row settles superseded without minting;
        // the newer save is the note's one live send.
        if (record.method === "notes/human/set" && (await this.#newerNoteSaveWaiting(transaction, record))) {
          await requestResult(store.put({ ...record, state: "superseded" }));
          retiredIds.push(record.clientMutationId);
          continue;
        }
        // Reconciliation NEVER re-enables the old row: an innocent parked
        // row mints a FRESH row in the background - barrier-free like
        // §4's recovery resends, fenced at birth by construction - and the
        // old row settles terminal "superseded", keeping its id and attempt
        // evidence for the receipt a real in-flight attempt can still
        // bring. The fresh row inherits the predecessor's wire id (the row
        // is delivery-uncertain: the daemon may already have applied it,
        // and a re-submission under the inherited id replays rather than
        // double-applies), and queues at the ref's tail.
        await requestResult(store.put({ ...record, state: "superseded" }));
        retiredIds.push(record.clientMutationId);
        const fresh = await this.#mintFreshRow(
          transaction,
          record,
          {
            stopEpoch: targetStopEpoch,
            displayedStopEpoch,
            bornCanceled: false,
            preserveOriginClientId: true,
          },
          retiredIds,
        );
        requeued.push(fresh);
      }
      return requeued;
    });
    if (retiredIds.length > 0) this.#retiredIdsListener?.(retiredIds);
    return restored;
  }

  async transferToRecovery(
    clientMutationId: string,
    recoveryKind: MutationRecoveryKind,
    recoveryReason?: string,
    options?: { wireDeliveryUncertain?: boolean },
  ): Promise<MutationRecoveryRecord | undefined> {
    return this.#write([OUTBOX_STORE, RECOVERY_STORE], "transferToRecovery", async (transaction) => {
      const outbox = transaction.objectStore(OUTBOX_STORE);
      const record = await requestResult<MutationOutboxRecord | undefined>(outbox.get(clientMutationId));
      if (!record) return undefined;
      // Note saves own their recovery state and can re-transfer the same row;
      // only a composer composition may not be replaced once edited.
      if (record.method !== "notes/human/set") {
        const existingRecovery = await requestResult<VersionedMutationRecoveryRecord | undefined>(
          transaction.objectStore(RECOVERY_STORE).get(clientMutationId),
        );
        if (existingRecovery && (existingRecovery.compositionRevision ?? 0) > 0) return undefined;
        // The wire coalesce: terminal refusals can arrive for BOTH halves of
        // a superseded wire pair - the original key and its minted
        // replacement carrying the inherited wire id - and transfers keyed
        // on the storage key alone would leave two independent sendable
        // copies of the SAME wire mutation, whose separate resends assign
        // independent wire ids and can submit the composition twice. When
        // the transfer's effective wire id already has a live recovery copy,
        // the refused row still leaves the outbox but no second copy is
        // created - the existing copy is the composition's one recovery.
        // Nothing here replaces a recovery record, so an independently
        // edited composition keeps the compositionRevision guard's existing
        // protection.
        // The wire-copy lookup is the wire family's own question (one home
        // with the settlement and resend sweeps, so it cannot diverge).
        const family = await this.#resolveWireFamily(transaction, effectiveWireId(record), { readRecovery: true });
        const wireCopy = family.recovery.find((existing) => existing.clientMutationId !== clientMutationId);
        if (wireCopy) {
          await requestResult(outbox.delete(clientMutationId));
          return wireCopy;
        }
      }
      const recovery: VersionedMutationRecoveryRecord = {
        ...record,
        recoveryKind,
        recoveryReason,
        compositionRevision: 0,
        ...(options?.wireDeliveryUncertain
          ? { wireDeliveryUncertain: true, originalWirePayload: { ...record.payload } }
          : {}),
      };
      await requestResult(transaction.objectStore(RECOVERY_STORE).put(recovery));
      await requestResult(outbox.delete(clientMutationId));
      return recovery;
    });
  }

  async editQueuedMessage(
    expected: MutationOutboxRecord,
    barrier: MutationStopBarrier,
  ): Promise<MutationRecoveryRecord | undefined> {
    return this.#write([OUTBOX_STORE, RECOVERY_STORE, SEQUENCE_STORE], "editQueuedMessage", async (transaction) => {
      const outbox = transaction.objectStore(OUTBOX_STORE);
      const record = await requestResult<MutationOutboxRecord | undefined>(outbox.get(expected.clientMutationId));
      if (!record || !matchesQueuedMessageSnapshot(record, expected) || !canEditQueuedMessage(record)) return undefined;
      if ((await this.#stopEpochOf(transaction, record.targetRef)) !== barrier.stopEpoch) return undefined;
      const existingRecovery = await requestResult<VersionedMutationRecoveryRecord | undefined>(
        transaction.objectStore(RECOVERY_STORE).get(record.clientMutationId),
      );
      if (
        existingRecovery &&
        existingRecovery.method !== "notes/human/set" &&
        (existingRecovery.compositionRevision ?? 0) > 0
      )
        return undefined;
      // The wire's recovery carriers - the same resolution the transfer
      // coalesce uses. A late refusal of the row's predecessor can leave an
      // UNEDITED recovery copy under the predecessor's key while this row
      // waits in the outbox, invisible to the own-key check above; editing
      // would then insert a SECOND copy of the same wire, and both ride the
      // persistence read into an activation's consolidation - the same text
      // and attachments merged twice into one active recovery. The ruling's
      // mechanical coalesce: the editing recovery this write creates under
      // the row's key IS the wire's one recovery, and the unedited carrier
      // retires in the same write. An INDEPENDENTLY EDITED carrier instead
      // owns the wire's editing session and this edit refuses - the own-key
      // guard's rule, one more transition.
      const wireCarrier = (
        await this.#resolveWireFamily(transaction, effectiveWireId(record), { readRecovery: true })
      ).recovery.find((existing) => existing.clientMutationId !== record.clientMutationId);
      if (wireCarrier && wireCarrier.method !== "notes/human/set" && (wireCarrier.compositionRevision ?? 0) > 0)
        return undefined;
      // Jesse's 2026-11-03 ruling on the #5583 collision: editing is the
      // user's call for uncertain rows too, and the duplicate problem is
      // solved mechanically, not by hiding the verb. An edit of a row whose
      // delivery is uncertain carries the wire's uncertainty identity into
      // the editing recovery - the same stamping transferToRecovery does -
      // so an UNCHANGED resend replays under the inherited wire id (journal
      // dedup, no double-apply) while changed content resends as the user's
      // new message under a fresh id. The predicate is uniform across the
      // two uncertainty shapes: a blockedUnknown row's own attempt may have
      // landed (the state's definition), and an inherited-wire row's
      // predecessor may have landed under the shared wire id. Canceled rows
      // and unattempted submitting rows are provably not delivered - no
      // marker, fresh resends.
      const uncertainEdit = record.state === "blockedUnknown" || effectiveWireId(record) !== record.clientMutationId;
      const recovery: VersionedMutationRecoveryRecord = {
        ...record,
        recoveryKind: "editing",
        compositionRevision: 0,
        ...(uncertainEdit ? { wireDeliveryUncertain: true, originalWirePayload: { ...record.payload } } : {}),
      };
      await requestResult(transaction.objectStore(RECOVERY_STORE).put(recovery));
      await requestResult(outbox.delete(record.clientMutationId));
      if (wireCarrier)
        await requestResult(transaction.objectStore(RECOVERY_STORE).delete(wireCarrier.clientMutationId));
      return recovery;
    });
  }

  async removeQueuedMessage(expected: MutationOutboxRecord, barrier: MutationStopBarrier): Promise<boolean> {
    const outcome = await this.#write(
      [OUTBOX_STORE, RECOVERY_STORE, SEQUENCE_STORE],
      "removeQueuedMessage",
      async (transaction) => {
        const outbox = transaction.objectStore(OUTBOX_STORE);
        const record = await requestResult<MutationOutboxRecord | undefined>(outbox.get(expected.clientMutationId));
        if (!record || !matchesQueuedMessageSnapshot(record, expected) || !canRemoveQueuedMessage(record))
          return undefined;
        if ((await this.#stopEpochOf(transaction, record.targetRef)) !== barrier.stopEpoch) return undefined;
        // Round-10 seam 2 (Jesse's 2026-11-03 ruling): removal ends the
        // composition, so it ends the wire - the round-9 routing at the #5583
        // seam. The removed row's wire siblings retire atomically in the same
        // write - the superseded predecessor in the outbox AND the wire's
        // UNEDITED recovery copies (a late refusal's carrier under the
        // predecessor's key, which would otherwise keep offering the removed
        // input for resend; the family sweep's unedited-only rule keeps an
        // independently edited draft, the user's live work) - or a delayed
        // terminal refusal for the wire would resurrect work the user
        // removed. The retired keys - INCLUDING the removed row's own, a live
        // strip row carrying a registration - reach the retiredIds
        // post-commit notification so the runtime clears without waiting for
        // a background read. No state filter here, unlike the mint's
        // generation bound: remove ends the composition entirely, exactly like
        // round-9's consolidation and discard, while the mint keeps a live
        // successor. If the wire's in-flight attempt genuinely lands, the
        // message may still appear in the conversation - it delivered;
        // nothing may re-queue it locally.
        const retiredIds = await this.#retireWireSiblings(transaction, effectiveWireId(record), {
          excludeKeys: new Set([record.clientMutationId]),
          sweepOptimistic: false,
          sweepRecovery: true,
        });
        await requestResult(outbox.delete(record.clientMutationId));
        return { retiredIds: [...retiredIds, record.clientMutationId] };
      },
    );
    if (!outcome) return false;
    if (outcome.retiredIds.length > 0) this.#retiredIdsListener?.(outcome.retiredIds);
    return true;
  }

  async getRecovery(clientMutationId: string): Promise<MutationRecoveryRecord | undefined> {
    return this.#read(RECOVERY_STORE, async (transaction) => {
      return requestResult<MutationRecoveryRecord | undefined>(
        transaction.objectStore(RECOVERY_STORE).get(clientMutationId),
      );
    });
  }

  // The displayed-ref half of the recovery read: an edited retargeted row's
  // recovery row carries the resumed ref as its durable destination while
  // the pane that must show it - the tray the strip's own Edit opened - is
  // the displayed ref it was retargeted from.
  async listRecoveryDisplayedAt(targetRef: string): Promise<MutationRecoveryRecord[]> {
    return this.#read(RECOVERY_STORE, async (transaction) => {
      const records = await requestResult<MutationRecoveryRecord[]>(transaction.objectStore(RECOVERY_STORE).getAll());
      return records
        .filter((record) => record.displayedRef === targetRef)
        .sort((left, right) => left.intentSequence - right.intentSequence);
    });
  }

  async listRecovery(targetRef?: string): Promise<MutationRecoveryRecord[]> {
    return this.#read(RECOVERY_STORE, async (transaction) => {
      const records = await requestResult<MutationRecoveryRecord[]>(transaction.objectStore(RECOVERY_STORE).getAll());
      return records
        .filter((record) => targetRef === undefined || record.targetRef === targetRef)
        .sort((left, right) => left.intentSequence - right.intentSequence);
    });
  }

  async updateRecoveryInput(
    clientMutationId: string,
    input: InputItem[],
    attachments?: MutationAttachment[],
    composerText?: string,
    composerMentions?: readonly ComposerMention[],
    consumeRecoveryIds?: readonly string[],
    localDraftRevisionId?: string,
    expectedCompositionRevision?: number,
    expectedRecoveryRevisions?: Readonly<Record<string, number>>,
    compositionOwnerId?: string,
  ): Promise<MutationRecoveryRecord | undefined> {
    const outcome = await this.#write([RECOVERY_STORE, OUTBOX_STORE], "updateRecoveryInput", async (transaction) => {
      const store = transaction.objectStore(RECOVERY_STORE);
      const record = await requestResult<VersionedMutationRecoveryRecord | undefined>(store.get(clientMutationId));
      if (!record) return undefined;
      const sources = [...new Set(consumeRecoveryIds)].filter((id) => id !== clientMutationId);
      const expected = new Map(Object.entries(expectedRecoveryRevisions ?? {}));
      // Existing scalar callers remain compatible for the owner only. A
      // supplied map is authoritative for every row in the transaction.
      if (expectedRecoveryRevisions === undefined && expectedCompositionRevision !== undefined)
        expected.set(clientMutationId, expectedCompositionRevision);
      // A consolidation must be based on the exact durable owner snapshot the
      // caller merged. Compare inside this transaction, before either writing
      // the replacement or deleting any secondary recovery rows.
      if (!expected.has(clientMutationId) || (record.compositionRevision ?? 0) !== expected.get(clientMutationId))
        return undefined;
      const consumedSources: MutationRecoveryRecord[] = [];
      for (const id of sources) {
        const source = await requestResult<MutationRecoveryRecord | undefined>(store.get(id));
        if (
          !source ||
          source.targetRef !== record.targetRef ||
          !expected.has(id) ||
          ((source as VersionedMutationRecoveryRecord).compositionRevision ?? 0) !== expected.get(id)
        )
          return undefined;
        consumedSources.push(source);
      }
      const next: VersionedMutationRecoveryRecord = {
        ...record,
        compositionRevision: (record.compositionRevision ?? 0) + 1,
        // The composition owner is durable identity: a write that does not name
        // a new owner keeps the row's existing seat rather than de-owning it.
        compositionOwnerId: compositionOwnerId ?? record.compositionOwnerId,
        payload: { ...record.payload, input },
        optimisticDisplay:
          record.optimisticDisplay && typeof record.optimisticDisplay === "object"
            ? { ...record.optimisticDisplay, input }
            : { method: record.method, input },
        attachments: attachments ?? record.attachments,
        composerText: composerText ?? record.composerText,
        localDraftRevisionId: localDraftRevisionId ?? record.localDraftRevisionId,
        // Locations belong to composerText. A full replacement can omit them
        // to clear old atoms, while a partial update preserves the pair.
        composerMentions: composerText === undefined ? (composerMentions ?? record.composerMentions) : composerMentions,
      };
      await requestResult(store.put(next));
      // Round-9 Medium: consuming a recovery row ends its wire's life in this
      // same write, exactly like the resend's consume. A consumed source's
      // superseded outbox predecessors - the rows whose delayed terminal
      // refusal would otherwise find no live carrier and mint a fresh
      // sendable recovery of content the merge already carries - retire
      // here, atomically with the consumption, and their ids reach the
      // runtime's registration clearing through the same retiredIds channel
      // every other retired row uses. The sweep reads the outbox only: the
      // survivor keeps its own wire, and no unedited same-wire recovery copy
      // can exist beside a live row (the transfer coalesce), so this write
      // needs no recovery read it did not already make.
      const retiredIds: string[] = [];
      const swept = new Set<string>();
      for (const source of consumedSources) {
        const wireId = effectiveWireId(source);
        if (swept.has(wireId)) continue;
        swept.add(wireId);
        retiredIds.push(
          ...(await this.#retireWireSiblings(transaction, wireId, {
            excludeKeys: new Set([clientMutationId, ...sources]),
            sweepOptimistic: false,
            sweepRecovery: false,
          })),
        );
      }
      for (const id of sources) await requestResult(store.delete(id));
      return { next, retiredIds };
    });
    if (!outcome) return undefined;
    if (outcome.retiredIds.length > 0) this.#retiredIdsListener?.(outcome.retiredIds);
    return outcome.next;
  }

  // A stale lease can no longer write its row, but the composition in the
  // composer is still the user's unsent work. Preserve it under a fresh
  // recovery owner without touching the peer's row. A stale row still holding
  // this exact composition already preserves it, so no duplicate is written.
  async preserveRecoveryComposition(
    base: MutationRecoveryRecord,
    input: InputItem[],
    attachments: MutationAttachment[],
    composerText: string,
    composerMentions?: readonly ComposerMention[],
    compositionOwnerId?: string,
    localDraftRevisionId?: string,
  ): Promise<MutationRecoveryRecord | undefined> {
    return this.#write(ENQUEUE_STORES, "preserveRecoveryComposition", async (transaction) => {
      const recoveryStore = transaction.objectStore(RECOVERY_STORE);
      const stale = await requestResult<VersionedMutationRecoveryRecord | undefined>(
        recoveryStore.get(base.clientMutationId),
      );
      if (stale && sameRecoveryComposition(stale, input, composerText, composerMentions, attachments)) return stale;
      const intentSequence = await this.#allocateSequence(transaction, base.targetRef);
      const clientMutationId = this.#createMutationId();
      await this.#assertMutationIdAvailable(transaction, clientMutationId);
      const record: VersionedMutationRecoveryRecord = {
        version: 1,
        clientMutationId,
        originClientId: ownClientId(),
        intentSequence,
        createdAt: this.#now(),
        targetRef: base.targetRef,
        threadId: base.threadId,
        instanceId: base.instanceId,
        method: base.method,
        // The preserved draft is INDEPENDENT work, not a replay of its base:
        // its payload id is its OWN fresh key, so the wire family of the
        // predecessor it was preserved from - whose late settlement sweeps
        // its own unedited copies - cannot reach it (round-6 H:1613: an
        // independently preserved composition is not a wire sibling).
        payload: { ...base.payload, ref: base.targetRef, input, clientMutationId },
        attachments,
        optimisticDisplay: { method: base.method, input },
        composerText,
        composerMentions,
        recoveryKind: "rejected",
        recoveryReason: base.recoveryReason,
        compositionRevision: 0,
        // A preserve that does not name a new owner keeps the preserved row
        // findable by the seat that authored its base composition.
        compositionOwnerId: compositionOwnerId ?? base.compositionOwnerId,
        // The displayed-session association is part of the composition: a
        // retargeted recovery's preserved replacement keeps the displayed
        // ref, or the displayed session's tray - which matches recoveries by
        // displayedRef - loses the composition after any reload.
        ...(base.displayedRef === undefined ? {} : { displayedRef: base.displayedRef }),
        localDraftRevisionId,
        state: "submitting",
        attempted: false,
        unresolvedAttempts: 0,
        stopEpochAtEnqueue: await this.#stopEpochOf(transaction, base.targetRef),
      };
      await requestResult(recoveryStore.put(record));
      return record;
    });
  }

  async discardRecovery(
    clientMutationId: string,
    shouldDiscard?: () => boolean,
    expectedCompositionRevision?: number,
  ): Promise<boolean> {
    const outcome = await this.#write([RECOVERY_STORE, OUTBOX_STORE], "discardRecovery", async (transaction) => {
      const store = transaction.objectStore(RECOVERY_STORE);
      const record = await requestResult<MutationRecoveryRecord | undefined>(store.get(clientMutationId));
      if (!record) return undefined;
      if (
        expectedCompositionRevision !== undefined &&
        ((record as VersionedMutationRecoveryRecord).compositionRevision ?? 0) !== expectedCompositionRevision
      )
        return undefined;
      // Evaluate immediately at the durable deletion boundary, after every
      // asynchronous prerequisite. Callers use this to invalidate a discard
      // that became stale while it was waiting to reach storage.
      if (shouldDiscard && !shouldDiscard()) return undefined;
      // Round-9 Medium: an explicit discard ends the composition, so it ends
      // the wire. The row's superseded outbox predecessors retire atomically
      // with the discard - or a delayed terminal refusal would resurrect the
      // discarded composition as a fresh sendable recovery - and their ids
      // reach the runtime's registration clearing through the same
      // retiredIds channel every other retired row uses.
      const retiredIds = await this.#retireWireSiblings(transaction, effectiveWireId(record), {
        excludeKeys: new Set([clientMutationId]),
        sweepOptimistic: false,
        sweepRecovery: false,
      });
      await requestResult(store.delete(clientMutationId));
      return { retiredIds };
    });
    if (!outcome) return false;
    if (outcome.retiredIds.length > 0) this.#retiredIdsListener?.(outcome.retiredIds);
    return true;
  }

  async resendRecovery(
    sourceIds: string | readonly string[],
    intent: MutationIntent,
    barrier?: MutationStopBarrier,
    expectedRecoveryRevisions?: Readonly<Record<string, number>>,
    compositionOwnerId?: string,
    displayedBarrier?: DisplayedRecoveryStopCapture,
  ): Promise<MutationOutboxRecord | undefined> {
    const result = await this.resendRecoveryResult(
      sourceIds,
      intent,
      barrier,
      expectedRecoveryRevisions,
      compositionOwnerId,
      displayedBarrier,
    );
    return result.status === "sent" ? result.record : undefined;
  }

  async resendRecoveryResult(
    sourceIds: string | readonly string[],
    intent: MutationIntent,
    barrier?: MutationStopBarrier,
    expectedRecoveryRevisions?: Readonly<Record<string, number>>,
    compositionOwnerId?: string,
    displayedBarrier?: DisplayedRecoveryStopCapture,
  ): Promise<MutationRecoveryResendResult> {
    if (!intent.targetRef.trim()) throw new Error("targetRef is required");
    return this.#write(ENQUEUE_STORES, "resendRecovery", async (transaction) => {
      const recoveryStore = transaction.objectStore(RECOVERY_STORE);
      const ids = [...new Set(typeof sourceIds === "string" ? [sourceIds] : sourceIds)];
      if (ids.length === 0) return { status: "refused" };
      let consumed = false;
      let refused = false;
      const consumedWireIds = new Set<string>();
      const consumedUncertainWireIds = new Set<string>();
      const consumedOriginals: Array<{ method: string; frozen: Record<string, unknown> | undefined }> = [];
      for (const id of ids) {
        const recovery = await requestResult<MutationRecoveryRecord | undefined>(recoveryStore.get(id));
        if (!recovery) {
          consumed = true;
          continue;
        }
        consumedWireIds.add(effectiveWireId(recovery));
        const frozen = (recovery as VersionedMutationRecoveryRecord).originalWirePayload;
        consumedOriginals.push({ method: recovery.method, frozen: frozen ? { ...frozen } : undefined });
        if ((recovery as VersionedMutationRecoveryRecord).wireDeliveryUncertain === true)
          consumedUncertainWireIds.add(effectiveWireId(recovery));
        const durableRevision = (recovery as VersionedMutationRecoveryRecord).compositionRevision ?? 0;
        // A caller that supplies no map is the legacy resend path and accepts
        // the durable row as it stands. A supplied map stays authoritative: a
        // missing id is a stale caller and is refused.
        const expected = expectedRecoveryRevisions === undefined ? durableRevision : expectedRecoveryRevisions[id];
        const sameOwner =
          compositionOwnerId !== undefined &&
          (recovery as VersionedMutationRecoveryRecord).compositionOwnerId === compositionOwnerId;
        if (
          recovery.targetRef !== intent.targetRef ||
          expected === undefined ||
          (durableRevision !== expected && !sameOwner)
        )
          refused = true;
      }
      if (refused) return { status: "refused" };
      if (consumed) return { status: "consumed" };
      const intentSequence = await this.#allocateSequence(transaction, intent.targetRef);
      const nextMutationId = this.#createMutationId();
      await this.#assertMutationIdAvailable(transaction, nextMutationId);
      // The resend is the user's deliberate post-Stop send (§4's exemption),
      // so its fresh row captures the target's current epoch like any new
      // submission: its own refused redeliveries stay transient instead of
      // fence-canceling against a Stop that predates the resend.
      const stopEpoch = await this.#stopEpochOf(transaction, intent.targetRef);
      const displayedStopEpoch =
        displayedBarrier === undefined
          ? 0
          : // An ordinary resend's displayed ref IS its destination: the one
            // read serves both halves.
            displayedBarrier.displayedRef === intent.targetRef
            ? stopEpoch
            : await this.#stopEpochOf(transaction, displayedBarrier.displayedRef);
      const attachments = intent.attachments.map((attachment) => ({
        ...attachment,
        presentationId: this.#createPresentationId(),
      }));
      // The wire-inheritance rule, one home with the mint's (#mintFreshRow):
      // a consumed recovery whose refusal never reached the daemon leaves
      // the wire delivery-uncertain - an outstanding attempt of the same wire
      // can still apply - so the resend carries the INHERITED wire id inside
      // its payload under its fresh storage key, and the daemon's journal
      // replays the re-submission instead of applying the composition twice.
      // Inheritance is only sound for a BYTE-IDENTICAL replay: the daemon
      // journals a client mutation by id together with its method and payload
      // hash, and a re-submission carrying either a different method or a
      // different payload is a MISMATCH it refuses before journaling - which
      // would land the new row back in a marked-uncertain recovery whose next
      // resend inherits the same id and mismatches again, forever (round-6
      // M:1761). Byte-identical means identical to the composition the daemon
      // JOURNALED, not to the record as it stands at consume time (round-7
      // High): the composer's autosave replaces payload.input in place, and
      // the Send path itself persists the composition before resending, so
      // the current payload - and the compositionRevision every such persist
      // bumps - cannot tell an edit from a no-op save. The frozen
      // originalWirePayload (stamped at transferToRecovery) is the immutable
      // reference; a marked record without one is a legacy row and fails
      // closed to a fresh id. Edited or rerouted content is NEW work under a
      // fresh id. A
      // journaled rejection resolved the wire authoritatively (the daemon
      // will never apply it), and a consolidation of several wires is merged
      // work the journal never saw as one composition: fresh ids too.
      const stripPayloadId = (payload: Record<string, unknown>): Record<string, unknown> => {
        const { clientMutationId: _id, ...rest } = payload;
        return rest;
      };
      const soleConsumedWireId = consumedWireIds.size === 1 ? [...consumedWireIds][0] : undefined;
      const replaysOriginalPayload = consumedOriginals.every(
        (original) =>
          original.frozen !== undefined &&
          sameJsonShape(stripPayloadId(intent.payload), stripPayloadId(original.frozen)),
      );
      // Round-8 High: a byte-identical replay of a delivery-uncertain wire
      // keeps the wire's identity AS A UNIT - id, method, and payload, the
      // three fields the daemon's journal replays together. The composer's
      // destination routing can rewrite the method (a Send pressed while the
      // destination is busy - busy with the wire's OWN outstanding sibling
      // attempt - routes as a queue), and judging identity on the rewritten
      // method would mint a fresh id for a composition whose sibling RPC can
      // still apply: double delivery through a client-side route choice. The
      // frozen original is the reference for the method too - an unchanged
      // replay submits under the ORIGINAL method with the inherited id, and
      // the route cannot mint a second wire for the same composition. Edited
      // content fails the payload compare and keeps the route's method as
      // NEW work under a fresh id, exactly as before.
      const firstOriginal = consumedOriginals[0];
      const uncertainReplay =
        soleConsumedWireId !== undefined &&
        firstOriginal !== undefined &&
        replaysOriginalPayload &&
        consumedUncertainWireIds.has(soleConsumedWireId) &&
        consumedOriginals.every((original) => original.method === firstOriginal.method);
      const replayMethod = uncertainReplay && firstOriginal !== undefined ? firstOriginal.method : undefined;
      const resendIntent = replayMethod === undefined ? intent : { ...intent, method: replayMethod };
      const inheritedWireId = uncertainReplay ? soleConsumedWireId : undefined;
      const record: MutationOutboxRecord = {
        ...resendIntent,
        payload: { ...resendIntent.payload, clientMutationId: inheritedWireId ?? nextMutationId },
        attachments,
        version: 1,
        clientMutationId: nextMutationId,
        // The resend is a fresh submission by whichever client performed it -
        // the recovering tab's own identity, not the original sender's.
        originClientId: ownClientId(),
        intentSequence,
        createdAt: this.#now(),
        state:
          (barrier !== undefined && stopEpoch > barrier.stopEpoch) ||
          (displayedBarrier !== undefined && displayedStopEpoch > displayedBarrier.stopEpoch)
            ? "canceled"
            : "submitting",
        // Retain the displayed-ref cancellation ownership past the enqueue:
        // a Stop pressed on the still-displayed session after this row
        // commits (while its dispatch waits out a reconnect) must still be
        // able to cancel it.
        ...(displayedBarrier === undefined
          ? {}
          : {
              displayedRef: displayedBarrier.displayedRef,
              displayedStopEpochAtEnqueue: displayedStopEpoch,
            }),
        attempted: false,
        stopEpochAtEnqueue: stopEpoch,
      };
      await requestResult(transaction.objectStore(OUTBOX_STORE).add(record));
      // Consume-time sibling retirement: transferToRecovery's wire coalesce
      // consults only LIVE recovery rows, so once this resend consumes a
      // recovery, a delayed terminal refusal for any other row of that wire
      // would find no wire copy and mint a NEW sendable recovery for work
      // this resend already delivers. Every remaining row of a consumed
      // recovery's wire retires in the same write transaction that consumes
      // it - the settled superseded predecessor, a replacement still
      // submitting mid-flight (its late outcomes land inert on a missing
      // row, the same semantics a late receipt against a settled row already
      // has), and a canceled duplicate whose chip leaves the strip with the
      // consumption, the same tail-position surface an explicit requeue
      // already has. The wire's one live row is the fresh row this resend
      // just created, excluded by key.
      // Consume-time sibling retirement through the one wire-family sweep:
      // every remaining row of a consumed wire retires in the same write
      // transaction that consumes it - outbox rows whatever their state (the
      // fresh resend row is the composition's one live row, excluded by key),
      // and unedited recovery copies of the wire beyond the consumed ones
      // (settlementRetiresRecovery keeps an edited composition). Optimistic
      // copies stay: a resend proves nothing about delivery, and accepted
      // copies are settlement's own business. The consumed ids themselves are
      // deleted below whatever their revision, by the consume contract.
      const retiredIds: string[] = [];
      const swept = new Set<string>();
      for (const wireId of consumedWireIds) {
        if (swept.has(wireId)) continue;
        swept.add(wireId);
        retiredIds.push(
          ...(await this.#retireWireSiblings(transaction, wireId, {
            excludeKeys: new Set([record.clientMutationId, ...ids]),
            sweepOptimistic: false,
            sweepRecovery: false,
          })),
        );
      }
      for (const id of ids) await requestResult(recoveryStore.delete(id));
      return { status: "sent", record, retiredIds };
    });
  }

  async nextDispatchable(targetRef: string): Promise<MutationOutboxRecord | undefined> {
    const records = await this.listOutbox(targetRef);
    for (const record of records) {
      if (record.state === "submitting") return record;
      // A canceled row provably never left the client, so it cannot be
      // reordered against the daemon and must not park what follows it —
      // including the interrupt that canceled it. A blockedUnknown head
      // stamped by the Stop fence is the same truth: the Stop is the reason
      // the row is not delivering, and the fence stops the row's DELIVERY,
      // not the queue — what follows it, most often the Stop's own
      // interrupt, must still reach the daemon or the session keeps running
      // past the Stop the user pressed. An unstamped blockedUnknown head is
      // genuine delivery uncertainty: the daemon may already have applied
      // it, so the FIFO stays closed behind it.
      if (record.state === "blockedUnknown" && record.stopFenced !== true) return undefined;
    }
    return undefined;
  }

  #upgrade(database: IDBDatabase): void {
    if (!database.objectStoreNames.contains(OUTBOX_STORE)) {
      const outbox = database.createObjectStore(OUTBOX_STORE, { keyPath: "clientMutationId" });
      outbox.createIndex(TARGET_SEQUENCE_INDEX, ["targetRef", "intentSequence"], { unique: true });
    }
    if (!database.objectStoreNames.contains(OPTIMISTIC_STORE)) {
      const optimistic = database.createObjectStore(OPTIMISTIC_STORE, { keyPath: "clientMutationId" });
      optimistic.createIndex(TARGET_SEQUENCE_INDEX, ["targetRef", "intentSequence"], { unique: true });
    }
    if (!database.objectStoreNames.contains(RECOVERY_STORE)) {
      const recovery = database.createObjectStore(RECOVERY_STORE, { keyPath: "clientMutationId" });
      recovery.createIndex(TARGET_SEQUENCE_INDEX, ["targetRef", "intentSequence"]);
    }
    if (!database.objectStoreNames.contains(SEQUENCE_STORE)) {
      database.createObjectStore(SEQUENCE_STORE, { keyPath: "targetRef" });
    }
  }

  // Record one open failure path. The try/catch is the whole contract: a
  // throwing reporter cannot change the durable open or upgrade outcome.
  #reportOpenDiagnostic(path: MutationOutboxOpenDiagnosticPath, versionchangeTransaction: boolean): void {
    try {
      this.#onOpenDiagnostic({
        database: this.#databaseName,
        version: DATABASE_VERSION,
        path,
        versionchangeTransaction,
      });
    } catch {
      // A diagnostic sink cannot change the storage outcome.
    }
  }

  async #read<T>(stores: string | string[], body: (transaction: IDBTransaction) => Promise<T>): Promise<T> {
    return this.#transaction(stores, "readonly", undefined, body);
  }

  async #write<T>(
    stores: string | string[],
    operation: MutationOutboxOperation | undefined,
    body: (transaction: IDBTransaction) => Promise<T>,
  ): Promise<T> {
    return this.#transaction(stores, "readwrite", operation, body);
  }

  // Every transaction registers with the projection work tracker when it
  // starts, which is what lets a flush wait for durable work whoever began it:
  // a Force stop's cancellation, a queue action, the dispatcher settling a
  // receipt once its RPC answers. No caller has to register the storage work
  // itself.
  #transaction<T>(
    stores: string | string[],
    mode: "readonly" | "readwrite",
    operation: MutationOutboxOperation | undefined,
    body: (transaction: IDBTransaction) => Promise<T>,
  ): Promise<T> {
    return trackProjectionWork(this.#runTransaction(stores, mode, operation, body));
  }

  async #runTransaction<T>(
    stores: string | string[],
    mode: "readonly" | "readwrite",
    operation: MutationOutboxOperation | undefined,
    body: (transaction: IDBTransaction) => Promise<T>,
  ): Promise<T> {
    // One attempt: a timeout fails this call (the caller retries by acting
    // again), and every later call attempts the open afresh.
    const database = await this.#connection.open();
    const transaction = database.transaction(stores, mode);
    const completed = transactionCompletion(transaction);
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stalledWrite = false;
    const deadline = new Promise<never>((_resolve, reject) => {
      timer = setTimeout(() => {
        this.#connection.retire(database);
        if (!tryAbortTransaction(transaction) && mode === "readwrite") {
          // abort() refuses a committing/finished transaction. A deadline
          // cannot prove rollback: keep the original submission pending
          // until its complete/abort event establishes its outcome.
          stalledWrite = true;
          this.#stalledWrites += 1;
          if (this.#stalledWrites === 1) this.#notifyWriteStalled(true);
          return;
        }
        reject(new MutationStorageTimeoutError());
      }, STORAGE_WAIT_MS);
    });
    try {
      // Bodies only await requests in this transaction, never timers or external
      // work. Their continuations (and this synchronous fault seam) run before
      // automatic commit, while failures can still abort the transaction.
      const work = body(transaction).then((result) => {
        if (operation) this.#beforeCommit?.(operation);
        return result;
      });
      // Promise.all rejects on either failure without waiting for the other
      // input. A terminal abort therefore releases even a stalled body, while
      // success requires both the result and the durable commit event.
      const [result] = await Promise.race([Promise.all([work, completed]), deadline]);
      return result;
    } catch (error) {
      tryAbortTransaction(transaction);
      throw error;
    } finally {
      clearTimeout(timer);
      if (stalledWrite) {
        this.#stalledWrites -= 1;
        if (this.#stalledWrites === 0) this.#notifyWriteStalled(false);
      }
    }
  }

  #notifyWriteStalled(waiting: boolean): void {
    this.#notifyQuietly(() => this.#onWriteStalled?.(waiting));
  }

  #notifyQuietly(notify: () => void): void {
    try {
      notify();
    } catch {
      // Status subscribers cannot change the durable transaction outcome.
    }
  }

  // The cross-store uniqueness invariant the port's enqueue contract states
  // (appwire-client .../mutation/outbox.ts): a freshly generated
  // clientMutationId must be absent from all three active stores before it is
  // written. The outbox's own `add` rejects a within-store duplicate as its
  // backstop; this catches a collision with a record that has already moved to
  // optimistic or recovery, which would otherwise let a later settlement keyed
  // on the id overwrite or discard that older active record. Throws before any
  // write, so the enclosing transaction aborts and its sequence allocation
  // rolls back; the id is rejected, never silently regenerated.
  async #assertMutationIdAvailable(transaction: IDBTransaction, clientMutationId: string): Promise<void> {
    const [outbox, optimistic, recovery] = await Promise.all([
      requestResult(transaction.objectStore(OUTBOX_STORE).get(clientMutationId)),
      requestResult(transaction.objectStore(OPTIMISTIC_STORE).get(clientMutationId)),
      requestResult(transaction.objectStore(RECOVERY_STORE).get(clientMutationId)),
    ]);
    if (outbox !== undefined || optimistic !== undefined || recovery !== undefined) {
      throw new Error(`clientMutationId is already active in the mutation outbox: ${clientMutationId}`);
    }
  }

  async #allocateSequence(transaction: IDBTransaction, targetRef: string): Promise<number> {
    const store = transaction.objectStore(SEQUENCE_STORE);
    const current = await requestResult<TargetSequence | undefined>(store.get(targetRef));
    const next = (current?.lastSequence ?? 0) + 1;
    // The spread preserves the row's stopEpoch: an allocation that dropped it
    // would silently reset the ref's stop barrier for every later enqueue.
    await requestResult(store.put({ ...current, targetRef, lastSequence: next } satisfies TargetSequence));
    return next;
  }

  async #stopEpochOf(transaction: IDBTransaction, targetRef: string): Promise<number> {
    const row = await requestResult<TargetSequence | undefined>(transaction.objectStore(SEQUENCE_STORE).get(targetRef));
    return row?.stopEpoch ?? 0;
  }

  // One Stop, one bump, inside the Stop's own cancel transaction: the bump is
  // what a later-committing enqueue compares its click-time capture against.
  async #bumpStopEpoch(transaction: IDBTransaction, targetRef: string): Promise<void> {
    const store = transaction.objectStore(SEQUENCE_STORE);
    const current = await requestResult<TargetSequence | undefined>(store.get(targetRef));
    await requestResult(
      store.put({
        ...current,
        targetRef,
        lastSequence: current?.lastSequence ?? 0,
        stopEpoch: (current?.stopEpoch ?? 0) + 1,
      } satisfies TargetSequence),
    );
  }

  // The dual-ref fence's ONE predicate, the comparison every fence site
  // consumes: whether a Stop landed on either ref the row crosses since its
  // enqueue-time baselines were captured. A missing baseline or a missing
  // displayed ref reads as never (zero), the conservative direction. The
  // optional epochs hand the predicate readings the caller's own transaction
  // already took (the reauthorization path validates and refreshes against
  // one read per ref), so folding a site here costs no extra sequence-store
  // reads.
  // Whether a newer save for the same note is already queued or waiting in
  // the outbox: the reconcile mint's ordering guard. "The same note" is the
  // session's human note - one note per target ref. A newer REJECTED save
  // sitting in recovery does not count: its retry is the editor's next
  // save, never a dispatch this row could overtake, and a canceled newer
  // save is the user's own durable decision against that text.
  async #newerNoteSaveWaiting(transaction: IDBTransaction, record: MutationOutboxRecord): Promise<boolean> {
    const records = await requestResult<MutationOutboxRecord[]>(transaction.objectStore(OUTBOX_STORE).getAll());
    return records.some(
      (candidate) =>
        candidate.method === "notes/human/set" &&
        candidate.targetRef === record.targetRef &&
        candidate.intentSequence > record.intentSequence &&
        (candidate.state === "submitting" || candidate.state === "blockedUnknown"),
    );
  }

  async #fenceCrossedSinceEnqueue(
    transaction: IDBTransaction,
    record: MutationOutboxRecord,
    epochs?: { stopEpoch?: number; displayedStopEpoch?: number },
  ): Promise<boolean> {
    const stopEpoch = epochs?.stopEpoch ?? (await this.#stopEpochOf(transaction, record.targetRef));
    if (stopEpoch > (record.stopEpochAtEnqueue ?? 0)) return true;
    if (record.displayedRef === undefined) return false;
    const displayedStopEpoch =
      epochs?.displayedStopEpoch ?? (await this.#stopEpochOf(transaction, record.displayedRef));
    return displayedStopEpoch > (record.displayedStopEpochAtEnqueue ?? 0);
  }

  // The enqueue shape every mint shares (mintRetryFromRow and the
  // reconcile's innocent re-queue): a sequence at the ref's tail, a fresh
  // storage key asserted absent from all three active stores, both stop
  // epochs read in the caller's own transaction and stamped as this row's
  // birth captures - immutable for its whole life - the composition's
  // identity (target, thread, instance, composer fields, attachments)
  // carried over from the predecessor, and `displayedRef` retained so a Stop
  // on the still-displayed session can still cancel the row. `inheritWireId`
  // names the wire-id rule: a blockedUnknown predecessor may already be
  // delivered, so the fresh row re-submits under its id and the daemon's
  // journal replays rather than double-applies; a canceled predecessor
  // provably never left any tab, so the fresh row carries its OWN fresh id.
  // The fresh row is born clean - no attempt evidence, no residual count, no
  // fence stamp - exactly like any enqueue's.
  async #mintFreshRow(
    transaction: IDBTransaction,
    source: MutationOutboxRecord,
    fresh: {
      stopEpoch: number;
      displayedStopEpoch: number | undefined;
      bornCanceled: boolean;
      // The automatic requeue (reconciliation's proven-absent mint) can run
      // in ANY tab, and the fresh row must keep the SENDER's provenance or
      // the sender's recordSubmittedHere never claims it and its own
      // Send/Queue routing mis-routes behind the send. An explicit Retry
      // press is a user-chosen actor and stamps the pressing tab.
      preserveOriginClientId?: boolean;
    },
    retiredIds: string[],
  ): Promise<MutationOutboxRecord> {
    const intentSequence = await this.#allocateSequence(transaction, source.targetRef);
    const clientMutationId = this.#createMutationId();
    await this.#assertMutationIdAvailable(transaction, clientMutationId);
    // The payload id IS the uncertainty marker. Inherit the EFFECTIVE wire
    // id whenever the source carries delivery-uncertain ancestry: a
    // blockedUnknown source may already be delivered, and a source whose
    // effective wire id differs from its own storage key carries an id
    // inherited from an uncertain predecessor - a replacement born canceled
    // by a Stop crossing at mint time is exactly that. Only a source whose
    // effective wire id is its own key - an ordinary canceled row that
    // provably never left any tab - takes a fresh id, so repeated mints
    // never lose the original wire identity the daemon's replay journal
    // knows.
    const inheritedWireId = effectiveWireId(source);
    const inheritWireId = source.state === "blockedUnknown" || inheritedWireId !== source.clientMutationId;
    const record: MutationOutboxRecord = {
      ...source,
      payload: {
        ...source.payload,
        clientMutationId: inheritWireId ? inheritedWireId : clientMutationId,
      },
      clientMutationId,
      originClientId: fresh.preserveOriginClientId ? (source.originClientId ?? ownClientId()) : ownClientId(),
      intentSequence,

      createdAt: this.#now(),
      state: fresh.bornCanceled ? "canceled" : "submitting",
      attempted: false,
      stopEpochAtEnqueue: fresh.stopEpoch,
      ...(source.displayedRef === undefined
        ? {}
        : {
            displayedRef: source.displayedRef,
            displayedStopEpochAtEnqueue: fresh.displayedStopEpoch,
          }),
    };
    // Born clean, exactly like any enqueue's row: no attempt evidence, no
    // residual count (a count a refusal's rollback wrote is what puts a
    // rolled-back row back through the gate - a fresh row needs no such
    // trace, and the missing field keeps its markAttempted byte-identical),
    // no fence stamp.
    delete record.unresolvedAttempts;
    delete record.stopFenced;
    await requestResult(transaction.objectStore(OUTBOX_STORE).add(record));
    // Round-10 Medium: the generation bound lives at the mint - the one
    // place the accumulation is created. While the daemon's journal write
    // keeps failing, each reconcile cycle (and each explicit Retry) settles
    // one full generation superseded - payload and image bytes retained -
    // and this mint queues another full copy under the inherited wire id;
    // unbounded, a prolonged outage accumulated byte-identical superseded
    // copies until the origin quota exhausted and further mints failed.
    // Every generation of one wire shares the same wire id, so a late
    // receipt or refusal reconciles through ANY one retained evidence row:
    // retiring the redundant OLDER superseded siblings here, in the mint's
    // own write, bounds retention at one evidence-bearing superseded row per
    // wire (the predecessor the caller just settled is the newest) - never
    // zero, so the late-outcome contract keeps its evidence. The swept ids
    // publish through the post-commit retiredIds channel like every other
    // retirement transition: the explicit Retry wrapper clears its own
    // predecessor, but a chain with ANY automatic cycle leaks - a
    // superseded generation's in-memory delivery registration is otherwise
    // cleared only by the background pin refresh, and failed reads leave
    // it armed on work that can never dispatch again. The mint is the one
    // home both arms share, so every key it retires publishes here.
    retiredIds.push(
      ...(await this.#retireWireSiblings(transaction, inheritedWireId, {
        excludeKeys: new Set([source.clientMutationId, record.clientMutationId]),
        sweepOptimistic: false,
        sweepRecovery: false,
        onlySuperseded: true,
      })),
    );
    return record;
  }

  // The ONE press-time capture both capture methods serve: a row (from
  // whichever store) and the stop epochs of its destination and displayed
  // refs, from ONE readonly snapshot. The sequences row is keyed by the ref,
  // which only the row read carries - but the pair must issue together, so
  // the epoch side reads the whole (small, one-row-per-ref) store and picks
  // the refs out of the result. It issues first so the pair cannot leave one
  // request's promise unconsumed: the row read is the request a caller's
  // abort seam can strike mid-creation, and both promises always meet
  // Promise.all. A missing row has no ref to fence (the caller's absent-row
  // refusal never reaches the comparison), and a missing displayed ref reads
  // zero - a capture consumer can compare both halves unconditionally.
  async #rowWithStopEpoch<R extends { targetRef: string }>(
    storeName: typeof OUTBOX_STORE | typeof RECOVERY_STORE,
    clientMutationId: string,
    displayedRefOf: (record: R | undefined) => string | undefined,
  ): Promise<{ record: R | undefined; stopEpoch: number; displayedStopEpoch: number }> {
    return this.#read([storeName, SEQUENCE_STORE], async (transaction) => {
      const sequencesRequest = requestResult<TargetSequence[]>(transaction.objectStore(SEQUENCE_STORE).getAll());
      const recordRequest = requestResult<R | undefined>(transaction.objectStore(storeName).get(clientMutationId));
      const [record, sequences] = await Promise.all([recordRequest, sequencesRequest]);
      const displayedRef = displayedRefOf(record);
      const stopEpoch = record
        ? (sequences.find((sequence) => sequence.targetRef === record.targetRef)?.stopEpoch ?? 0)
        : 0;
      const displayedStopEpoch = displayedRef
        ? (sequences.find((sequence) => sequence.targetRef === displayedRef)?.stopEpoch ?? 0)
        : 0;
      return { record, stopEpoch, displayedStopEpoch };
    });
  }
}
