import type { InputItem } from "@evener/appwire-client";
import { ownClientId } from "./mutationClientIdentity";
import type {
  MutationAttachment,
  MutationIntent,
  MutationOptimisticRecord,
  MutationOutboxRecord,
  MutationRecord,
  MutationRecoveryKind,
  MutationRecoveryRecord,
  MutationStopBarrier,
} from "./mutationOutbox";
import { createSecureUUID } from "./secureUUID";

type MutationOutboxOperation =
  | "markAttempted"
  | "enqueueIntent"
  | "enqueueInterruptAndCancel"
  | "cancelUnattempted"
  | "discardCanceled"
  | "releaseCanceled"
  | "settleReceipt"
  | "transferToRecovery"
  | "updateRecoveryInput"
  | "discardRecovery"
  | "resendRecovery";

export interface MutationOutboxIndexedDBOptions {
  indexedDB?: IDBFactory;
  databaseName?: string;
  createMutationId?: () => string;
  createPresentationId?: () => string;
  now?: () => number;
  onWriteStalled?: (waiting: boolean) => void;
  // Storage-fault seam used to prove IndexedDB rollback at commit boundaries.
  beforeCommit?: (operation: MutationOutboxOperation) => void;
}

const DATABASE_NAME = "evener-mutation-outbox";
// Version 3 is a compatibility fence, not a schema migration: the upgrade
// handler below is unchanged and purely additive. The deployed version-2 code
// reads a canceled row as a non-submitting FIFO head and stalls the ref's
// queue, so this build must not share a database with it - a version-2 open
// against this database fails with VersionError, and the old tab fails closed
// (storage-unavailable errors) instead of silently stalling
// (docs/design/stop-cancellation-outbox.md §8).
const DATABASE_VERSION = 3;
const OUTBOX_STORE = "outbox";
const OPTIMISTIC_STORE = "optimistic";
const RECOVERY_STORE = "recovery";
const SEQUENCE_STORE = "sequences";
const TARGET_SEQUENCE_INDEX = "byTargetSequence";
const STORAGE_WAIT_MS = 10_000;
// The store scope every enqueue transaction opens: the three active record
// stores plus the sequence store. All three record stores are locked so the
// cross-store clientMutationId uniqueness check (#assertMutationIdAvailable)
// reads and writes under one transaction.
const ENQUEUE_STORES = [OUTBOX_STORE, OPTIMISTIC_STORE, RECOVERY_STORE, SEQUENCE_STORE];

export class MutationStorageTimeoutError extends Error {
  constructor() {
    super("Browser message storage is not responding. Your draft has been kept. Try again when storage recovers.");
    this.name = "MutationStorageTimeoutError";
  }
}

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

function requestResult<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error ?? new Error("IndexedDB request failed")), {
      once: true,
    });
  });
}

function transactionCompletion(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.addEventListener("complete", () => resolve(), { once: true });
    transaction.addEventListener(
      "abort",
      () => reject(transaction.error ?? new Error("IndexedDB transaction aborted")),
      { once: true },
    );
    transaction.addEventListener(
      "error",
      () => reject(transaction.error ?? new Error("IndexedDB transaction failed")),
      { once: true },
    );
  });
}

// MutationOutboxIndexedDB serializes sequence allocation with each durable
// transition. Cross-tab correctness comes from IndexedDB readwrite transaction
// ordering rather than a browser lease or leader election.
export class MutationOutboxIndexedDB {
  readonly #indexedDB: IDBFactory;
  readonly #databaseName: string;
  readonly #createMutationId: () => string;
  readonly #createPresentationId: () => string;
  readonly #now: () => number;
  readonly #beforeCommit: ((operation: MutationOutboxOperation) => void) | undefined;
  readonly #onWriteStalled: ((waiting: boolean) => void) | undefined;
  #supersededDiscardListener: ((targetRef: string) => void) | undefined;
  #stalledWrites = 0;
  #databasePromise: Promise<IDBDatabase> | undefined;
  #database: IDBDatabase | undefined;

  constructor(options: MutationOutboxIndexedDBOptions = {}) {
    const factory = options.indexedDB ?? globalThis.indexedDB;
    if (!factory) throw new Error("IndexedDB is unavailable");
    this.#indexedDB = factory;
    this.#databaseName = options.databaseName ?? DATABASE_NAME;
    this.#createMutationId = options.createMutationId ?? createSecureUUID;
    this.#createPresentationId = options.createPresentationId ?? createSecureUUID;
    this.#now = options.now ?? Date.now;
    this.#beforeCommit = options.beforeCommit;
    this.#onWriteStalled = options.onWriteStalled;
  }

  close(): void {
    this.#database?.close();
    this.#database = undefined;
    this.#databasePromise = undefined;
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
      };
      await requestResult(transaction.objectStore(OUTBOX_STORE).add(record));
      return record;
    });
  }

  // The click-time half of §4's stop barrier: what an enqueuing tab reads at
  // its user's click, before its durable write issues. Fresh from the
  // ref's sequence row, never cached - another tab's Stop is invisible to
  // this tab's memory.
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
  ): Promise<{ record: MutationOutboxRecord | undefined; stopEpoch: number }> {
    return this.#read([OUTBOX_STORE, SEQUENCE_STORE], async (transaction) => {
      // The sequences row is keyed by the ref, which only the row read
      // carries - but the pair must issue together, so the epoch side reads
      // the whole (small, one-row-per-ref) store and picks the row's ref out of
      // the result. It issues first so the pair cannot leave one request's
      // promise unconsumed: the row read is the request a caller's abort seam
      // can strike mid-creation, and both promises always meet Promise.all.
      const sequencesRequest = requestResult<TargetSequence[]>(transaction.objectStore(SEQUENCE_STORE).getAll());
      const recordRequest = requestResult<MutationOutboxRecord | undefined>(
        transaction.objectStore(OUTBOX_STORE).get(clientMutationId),
      );
      const [record, sequences] = await Promise.all([recordRequest, sequencesRequest]);
      // A missing row has no ref to fence; the caller's absent-row refusal
      // never reaches the comparison.
      const stopEpoch = record
        ? (sequences.find((sequence) => sequence.targetRef === record.targetRef)?.stopEpoch ?? 0)
        : 0;
      return { record, stopEpoch };
    });
  }

  // Stop's durable write: the ref's cancelable rows turn "canceled" in the
  // same readwrite transaction that enqueues the turn/interrupt record, so
  // the user's click is the cancel moment and both land or neither does.
  // The cancellations are written before the interrupt is added so the scan
  // cannot cancel the interrupt itself, and an aborted commit rolls both back.
  async enqueueInterruptAndCancel(intent: MutationIntent): Promise<MutationOutboxRecord> {
    if (!intent.targetRef.trim()) throw new Error("targetRef is required");
    return this.#write(ENQUEUE_STORES, "enqueueInterruptAndCancel", async (transaction) => {
      const outbox = transaction.objectStore(OUTBOX_STORE);
      await this.#cancelUnattempted(transaction, intent.targetRef);
      await this.#bumpStopEpoch(transaction, intent.targetRef);
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
        state: "submitting",
        attempted: false,
      };
      await requestResult(outbox.add(record));
      return record;
    });
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
      if (record.targetRef !== targetRef) continue;
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

  // The one release of a canceled row: an explicit user Retry. Background
  // reconciliation and reopen paths never reach this — it transitions only
  // canceled -> submitting and refuses every other state. The optional
  // barrier is the Retry click's stop-epoch capture (getOutboxWithStopEpoch,
  // the release twin of enqueueIntent's): a Stop whose cancel transaction
  // committed between the capture and this release bumped the epoch past the
  // capture, and its cancel scan skipped the row (already canceled) - this
  // write is the only thing left that could resurrect it, so it refuses and
  // the row stays canceled for the newer Stop. A capture taken after the
  // newest Stop compares equal and releases: that Retry is §4's deliberate
  // post-Stop send. A caller passing no barrier at all is unchanged.
  async releaseCanceled(clientMutationId: string, barrier?: MutationStopBarrier): Promise<boolean> {
    return this.#write([OUTBOX_STORE, SEQUENCE_STORE], "releaseCanceled", async (transaction) => {
      const store = transaction.objectStore(OUTBOX_STORE);
      const record = await requestResult<MutationOutboxRecord | undefined>(store.get(clientMutationId));
      if (record?.state !== "canceled") return false;
      if (barrier !== undefined && (await this.#stopEpochOf(transaction, record.targetRef)) > barrier.stopEpoch) {
        return false;
      }
      await requestResult(store.put({ ...record, state: "submitting" }));
      return true;
    });
  }

  async listOutbox(targetRef?: string): Promise<MutationOutboxRecord[]> {
    return this.#read(OUTBOX_STORE, async (transaction) => {
      const records = await requestResult<MutationOutboxRecord[]>(transaction.objectStore(OUTBOX_STORE).getAll());
      return records
        .filter((record) => targetRef === undefined || record.targetRef === targetRef)
        .sort((left, right) => left.intentSequence - right.intentSequence);
    });
  }

  async listTargetRefs(): Promise<string[]> {
    const [outbox, optimistic] = await Promise.all([this.listOutbox(), this.listOptimistic()]);
    return [...new Set([...outbox, ...optimistic].map((record) => record.targetRef))].sort();
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
    let settledSource: MutationRecord | undefined;
    const settled = await this.#write(
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
        const source = outboxRecord ?? recoveryRecord ?? optimisticRecord;
        if (!source) return false;
        await this.#discardSupersededNoteRecovery(transaction, source);
        settledSource = source;

        const display = source.optimisticDisplay;
        // A "pending" receipt means accepted but not yet described by
        // authoritative state. The daemon reports pending for exactly the
        // mutations whose acceptance a read cannot yet prove - every
        // input-bearing turn verb AND notes/human/set
        // (acceptedClientMutationProjection) - and this store owes each of them
        // a kept copy until reconcileIdentities settles it from a later read's
        // authoritative ids. For the turn verbs the kept copy is the optimistic
        // display itself (an input array renders the pending row); a note
        // carries no display (production enqueues notes with null), but its kept
        // copy is what tells humanNoteDrafts' post-retry lookup an accepted save
        // is pending rather than settled-elsewhere. A receipt-only control (a
        // Stop) settles terminal at acceptance and keeps no copy: its effect is
        // already readable in the session's own state, so its pending receipts
        // drop the row exactly as before.
        const retainsAcceptedCopy =
          projectionState === "pending" &&
          ((display !== null && typeof display === "object" && "input" in display && Array.isArray(display.input)) ||
            source.method === "notes/human/set");
        if (retainsAcceptedCopy) {
          const accepted: MutationOptimisticRecord = {
            version: source.version,
            clientMutationId: source.clientMutationId,
            // Provenance survives the outbox -> optimistic transition: dropping
            // it here would make the accepted-but-unreflected mutation
            // unattributed, and every tab would claim it as its own send.
            originClientId: source.originClientId,
            intentSequence: source.intentSequence,
            createdAt: source.createdAt,
            targetRef: source.targetRef,
            threadId: source.threadId,
            instanceId: source.instanceId,
            method: source.method,
            payload: source.payload,
            attachments: source.attachments,
            optimisticDisplay: source.optimisticDisplay,
            state: "accepted",
          };
          await requestResult(optimistic.put(accepted));
        } else if (optimisticRecord) {
          await requestResult(optimistic.delete(clientMutationId));
        }
        if (outboxRecord) await requestResult(outbox.delete(clientMutationId));
        if (recoveryRecord) await requestResult(recovery.delete(clientMutationId));
        return true;
      },
    );
    this.#discardSupersededCanceledNotesAfterSettle(settled ? settledSource : undefined);
    return settled;
  }

  async settleApplied(clientMutationId: string): Promise<boolean> {
    let settledSource: MutationRecord | undefined;
    const settled = await this.#write(
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
        if (!outboxRecord && !optimisticRecord && !recoveryRecord) return false;
        const source = outboxRecord ?? optimisticRecord ?? recoveryRecord;
        await this.#discardSupersededNoteRecovery(transaction, source);
        if (outboxRecord) await requestResult(outbox.delete(clientMutationId));
        if (optimisticRecord) await requestResult(optimistic.delete(clientMutationId));
        if (recoveryRecord) await requestResult(recovery.delete(clientMutationId));
        settledSource = source;
        return true;
      },
    );
    this.#discardSupersededCanceledNotesAfterSettle(settled ? settledSource : undefined);
    return settled;
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
        record.intentSequence < source.intentSequence
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
        try {
          this.#supersededDiscardListener?.(source.targetRef);
        } catch {
          // A listener cannot change the durable transaction's outcome.
        }
      })
      .catch(() => {
        // Left for the next settle, clear, or delete.
      });
  }

  // Commit attempt evidence before transport so another tab or a reload cannot
  // mistake a possibly delivered mutation for an unsent intent.
  async markAttempted(clientMutationId: string): Promise<boolean> {
    return this.#write(OUTBOX_STORE, "markAttempted", async (transaction) => {
      const store = transaction.objectStore(OUTBOX_STORE);
      const record = await requestResult<MutationOutboxRecord | undefined>(store.get(clientMutationId));
      if (record?.state !== "submitting") return false;
      await requestResult(store.put({ ...record, attempted: true }));
      return true;
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
      // A canceled row is the user's durable decision; an uncertain-outcome
      // write must not reclassify it back into something restoreProvenAbsent
      // could reopen.
      if (record.state === "canceled") return false;
      if (record.state !== state) await requestResult(store.put({ ...record, state }));
      return true;
    });
  }

  // Reopen unresolved records after a live authoritative read. Missing IDs
  // are not proof of non-delivery: bounded transcripts omit older work.
  // Preserve the original mutation ID and entire payload so the daemon's
  // durable journal can replay its receipt, or the original instance fence
  // can reject a retry after a clear. Known IDs stay on the receipt path.
  // Returns the restored IDs.
  async restoreProvenAbsent(targetRef: string, authoritativeIds: ReadonlySet<string>): Promise<string[]> {
    return this.#write(OUTBOX_STORE, undefined, async (transaction) => {
      const store = transaction.objectStore(OUTBOX_STORE);
      const records = await requestResult<MutationOutboxRecord[]>(store.getAll());
      const restored: string[] = [];
      for (const record of records) {
        if (record.targetRef !== targetRef) continue;
        if (record.state !== "blockedUnknown") continue;
        if (authoritativeIds.has(record.clientMutationId)) continue;
        await requestResult(store.put({ ...record, state: "submitting" }));
        restored.push(record.clientMutationId);
      }
      return restored;
    });
  }

  async transferToRecovery(
    clientMutationId: string,
    recoveryKind: MutationRecoveryKind,
    recoveryReason?: string,
  ): Promise<MutationRecoveryRecord | undefined> {
    return this.#write([OUTBOX_STORE, RECOVERY_STORE], "transferToRecovery", async (transaction) => {
      const outbox = transaction.objectStore(OUTBOX_STORE);
      const record = await requestResult<MutationOutboxRecord | undefined>(outbox.get(clientMutationId));
      if (!record) return undefined;
      const recovery: MutationRecoveryRecord = { ...record, recoveryKind, recoveryReason };
      await requestResult(transaction.objectStore(RECOVERY_STORE).put(recovery));
      await requestResult(outbox.delete(clientMutationId));
      return recovery;
    });
  }

  async getRecovery(clientMutationId: string): Promise<MutationRecoveryRecord | undefined> {
    return this.#read(RECOVERY_STORE, async (transaction) => {
      return requestResult<MutationRecoveryRecord | undefined>(
        transaction.objectStore(RECOVERY_STORE).get(clientMutationId),
      );
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
  ): Promise<MutationRecoveryRecord | undefined> {
    return this.#write(RECOVERY_STORE, "updateRecoveryInput", async (transaction) => {
      const store = transaction.objectStore(RECOVERY_STORE);
      const record = await requestResult<MutationRecoveryRecord | undefined>(store.get(clientMutationId));
      if (!record) return undefined;
      const next: MutationRecoveryRecord = {
        ...record,
        payload: { ...record.payload, input },
        optimisticDisplay:
          record.optimisticDisplay && typeof record.optimisticDisplay === "object"
            ? { ...record.optimisticDisplay, input }
            : { method: record.method, input },
        attachments: attachments ?? record.attachments,
        composerText: composerText ?? record.composerText,
      };
      await requestResult(store.put(next));
      return next;
    });
  }

  async discardRecovery(clientMutationId: string, shouldDiscard?: () => boolean): Promise<boolean> {
    return this.#write(RECOVERY_STORE, "discardRecovery", async (transaction) => {
      const store = transaction.objectStore(RECOVERY_STORE);
      const record = await requestResult<MutationRecoveryRecord | undefined>(store.get(clientMutationId));
      if (!record) return false;
      // Evaluate immediately at the durable deletion boundary, after every
      // asynchronous prerequisite. Callers use this to invalidate a discard
      // that became stale while it was waiting to reach storage.
      if (shouldDiscard && !shouldDiscard()) return false;
      await requestResult(store.delete(clientMutationId));
      return true;
    });
  }

  async resendRecovery(clientMutationId: string, intent: MutationIntent): Promise<MutationOutboxRecord | undefined> {
    if (!intent.targetRef.trim()) throw new Error("targetRef is required");
    return this.#write(ENQUEUE_STORES, "resendRecovery", async (transaction) => {
      const recoveryStore = transaction.objectStore(RECOVERY_STORE);
      const recovery = await requestResult<MutationRecoveryRecord | undefined>(recoveryStore.get(clientMutationId));
      if (!recovery) return undefined;
      const intentSequence = await this.#allocateSequence(transaction, intent.targetRef);
      const nextMutationId = this.#createMutationId();
      await this.#assertMutationIdAvailable(transaction, nextMutationId);
      const attachments = intent.attachments.map((attachment) => ({
        ...attachment,
        presentationId: this.#createPresentationId(),
      }));
      const record: MutationOutboxRecord = {
        ...intent,
        payload: { ...intent.payload, clientMutationId: nextMutationId },
        attachments,
        version: 1,
        clientMutationId: nextMutationId,
        // The resend is a fresh submission by whichever client performed it -
        // the recovering tab's own identity, not the original sender's.
        originClientId: ownClientId(),
        intentSequence,
        createdAt: this.#now(),
        state: "submitting",
        attempted: false,
      };
      await requestResult(transaction.objectStore(OUTBOX_STORE).add(record));
      await requestResult(recoveryStore.delete(clientMutationId));
      return record;
    });
  }

  async nextDispatchable(targetRef: string): Promise<MutationOutboxRecord | undefined> {
    const records = await this.listOutbox(targetRef);
    for (const record of records) {
      if (record.state === "submitting") return record;
      // A canceled row provably never left the client, so it cannot be
      // reordered against the daemon and must not park what follows it —
      // including the interrupt that canceled it. A blockedUnknown head is
      // different: the daemon may already have applied it, so the FIFO stays
      // closed behind it.
      if (record.state === "blockedUnknown") return undefined;
    }
    return undefined;
  }

  async #open(): Promise<IDBDatabase> {
    if (this.#database) return this.#database;
    if (this.#databasePromise) return this.#databasePromise;
    const opening = new Promise<IDBDatabase>((resolve, reject) => {
      const request = this.#indexedDB.open(this.#databaseName, DATABASE_VERSION);
      let abandoned = false;
      let upgradeTransaction: IDBTransaction | null = null;
      const fail = (error: unknown) => {
        abandoned = true;
        clearTimeout(timer);
        try {
          // Release the database open lock if its schema upgrade is still active.
          upgradeTransaction?.abort();
        } catch {
          // A completed upgrade cannot be aborted; late success closes its connection.
        }
        reject(error);
      };
      const timer = setTimeout(() => fail(new MutationStorageTimeoutError()), STORAGE_WAIT_MS);
      request.addEventListener(
        "upgradeneeded",
        () => {
          upgradeTransaction = request.transaction;
          if (abandoned || this.#databasePromise !== opening) {
            upgradeTransaction?.abort();
            return;
          }
          const database = request.result;
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
        },
        { once: true },
      );
      request.addEventListener(
        "success",
        () => {
          clearTimeout(timer);
          const database = request.result;
          if (abandoned || this.#databasePromise !== opening) {
            database.close();
            reject(new Error("Mutation outbox connection was closed"));
            return;
          }
          this.#database = database;
          database.addEventListener("versionchange", () => this.#retire(database));
          database.addEventListener("close", () => this.#retire(database));
          resolve(database);
        },
        { once: true },
      );
      request.addEventListener("error", () => fail(request.error ?? new Error("Unable to open mutation outbox")), {
        once: true,
      });
      request.addEventListener("blocked", () => fail(new Error("Mutation outbox upgrade is blocked")), {
        once: true,
      });
    });
    this.#databasePromise = opening;
    try {
      return await opening;
    } catch (error) {
      if (this.#databasePromise === opening) this.#databasePromise = undefined;
      throw error;
    }
  }

  #retire(database: IDBDatabase): void {
    database.close();
    if (this.#database !== database) return;
    this.#database = undefined;
    this.#databasePromise = undefined;
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

  async #transaction<T>(
    stores: string | string[],
    mode: "readonly" | "readwrite",
    operation: MutationOutboxOperation | undefined,
    body: (transaction: IDBTransaction) => Promise<T>,
  ): Promise<T> {
    const database = await this.#open();
    const transaction = database.transaction(stores, mode);
    const completed = transactionCompletion(transaction);
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stalledWrite = false;
    const deadline = new Promise<never>((_resolve, reject) => {
      timer = setTimeout(() => {
        this.#retire(database);
        try {
          transaction.abort();
        } catch {
          if (mode === "readwrite") {
            // abort() refuses a committing/finished transaction. A deadline
            // cannot prove rollback: keep the original submission pending
            // until its complete/abort event establishes its outcome.
            stalledWrite = true;
            this.#stalledWrites += 1;
            if (this.#stalledWrites === 1) this.#notifyWriteStalled(true);
            return;
          }
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
      try {
        transaction.abort();
      } catch {
        // The transaction already completed; preserve the original failure.
      }
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
    try {
      this.#onWriteStalled?.(waiting);
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
}
