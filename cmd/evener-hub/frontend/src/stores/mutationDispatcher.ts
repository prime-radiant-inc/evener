import type {
  AppwireClientLike,
  MethodName,
  MutationReceipt,
  NotesHumanSetResponse,
  ThreadClearResponse,
} from "@evener/appwire-client";
import { mutationErrorData, WireError } from "@evener/appwire-client";
import type { MutationOutboxRecord, MutationRecord } from "./mutationOutbox";
import type { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

// A caller's own fresh reading of the server's queue for one target ref,
// translated from whatever wire shape it came from (a queueChanged push, a
// thread/read hydrate, a thread/clear response). `ids` is the queue's full
// clientMutationId membership when known - `undefined` means coverage is
// unknown (a legacy server push that never populates it, still a real,
// reachable path: see #1704/#1705) and reconcileQueueSnapshot must not
// guess, never treating "unknown" as "empty". `revision` orders snapshots
// for the same target so a stale or reordered one is ignored rather than
// acted on, but only within one session instance: `instanceId` is that
// session's own identity (the thread's instanceId, falling back to its
// threadId - the same fence expectedInstanceId uses), since a thread/clear
// installs a new instance whose revision counter restarts, and a revision
// is meaningless compared across two different instances.
// `authoritative` is the caller's own answer to "is this data source live
// and trustworthy right now", separate from ids coverage - false for a
// saved/incompatible/not-loaded hydrate snapshot.
// `cut` is peekAcceptCounter(targetRef)'s reading, captured by the CALLER at
// this snapshot's true arrival (the wire notification/response first
// reaching the client) rather than by this method at whatever later moment
// it is actually called - the two can differ (a hydrate reconciles identities
// and restores proven-absent records before ever calling this), and a scan
// gated on a cut read late could already include an accept that happened
// during that gap, wrongly retiring it (#1717's Medium 1, round 4). The scan
// retires only optimistic records this target accepted at or before `cut`
// (the record's own durable intentSequence) - never one accepted after it,
// however chain-queueing happens to order the two.
export interface QueueSnapshot {
  ids: ReadonlySet<string> | undefined;
  revision: number;
  authoritative: boolean;
  instanceId: string;
  cut: number;
}

export interface MutationDispatcherOptions {
  getClient: (targetRef: string) => AppwireClientLike | null | undefined;
  onStorageChange?: (targetRefs: string[]) => void;
  onBlockedMutation?: (targetRef: string, client: AppwireClientLike) => void;
  onClearResponse?: (targetRef: string, response: ThreadClearResponse) => void;
  onHumanNoteResponse?: (record: MutationOutboxRecord, response: NotesHumanSetResponse) => void;
  // Capture response authority immediately before each transport attempt,
  // after any reconnect hydration, rather than once at durable enqueue.
  prepareHumanNoteResponse?: (record: MutationOutboxRecord) => (response: NotesHumanSetResponse) => void;
  onHumanNoteReconciled?: (record: MutationRecord) => void;
}

export class MutationDispatcher {
  readonly #storage: MutationOutboxIndexedDB;
  readonly #getClient: MutationDispatcherOptions["getClient"];
  readonly #onStorageChange: NonNullable<MutationDispatcherOptions["onStorageChange"]>;
  readonly #onBlockedMutation: NonNullable<MutationDispatcherOptions["onBlockedMutation"]>;
  readonly #onClearResponse: NonNullable<MutationDispatcherOptions["onClearResponse"]>;
  readonly #prepareHumanNoteResponse: NonNullable<MutationDispatcherOptions["prepareHumanNoteResponse"]>;
  readonly #onHumanNoteReconciled: NonNullable<MutationDispatcherOptions["onHumanNoteReconciled"]>;
  readonly #dispatching = new Map<string, Promise<void>>();
  readonly #requestedRuns = new Map<string, number>();
  readonly #queueReconciliations = new Map<string, Promise<unknown>>();
  // One state record per target: the instance this target is currently on,
  // the highest revision reconciled for it, and the accept-cut as of that
  // reconciliation (kept for the same reason revision is - a record of where
  // this target's own baseline last stood, not itself compared against a
  // record's stamp; each snapshot's OWN cut does that, fresh every time).
  // `revision` is the highest revision actually RECONCILED (bumped only once
  // reconcileIdentities/settleOptimisticAbsent has both succeeded, so a
  // failed write's retry at the same revision is not discarded as stale).
  // `observedRevision` is the highest revision merely SEEN, including an
  // unknown-coverage snapshot this target could do nothing with - without
  // it, a legacy push at revision 5 left `revision` unmoved, so a delayed
  // KNOWN snapshot at revision 4 still read as newer and acted on stale data
  // (RoboRev's Medium, #1705 round 5). Always >= `revision`.
  readonly #queueState = new Map<string, { instance: string; revision: number; observedRevision: number; cut: number }>();
  readonly #supersededQueueInstances = new Map<string, Set<string>>();
  // The target's own last-accepted intentSequence (MutationOutboxRecord's
  // durable, gapless per-target order - #allocateSequence, copied onto the
  // accepted optimistic record by settleReceipt), advanced only by
  // #recordAccepted. Unlike an in-memory-only counter, this is rehydrated
  // from storage on first use (#ensureAcceptSequencesRehydrated), so a page
  // reload or a second tab's own fresh dispatcher still knows what a
  // PREVIOUS dispatcher instance already accepted - see QueueSnapshot's own
  // `cut` for why a caller reads this (peekAcceptCounter) at a snapshot's
  // true arrival rather than this dispatcher inferring it later.
  readonly #lastAcceptedIntentSequence = new Map<string, number>();
  #acceptSequenceRehydration: Promise<void> | undefined;

  constructor(storage: MutationOutboxIndexedDB, options: MutationDispatcherOptions) {
    this.#storage = storage;
    this.#getClient = options.getClient;
    this.#onStorageChange = options.onStorageChange ?? (() => undefined);
    this.#onBlockedMutation = options.onBlockedMutation ?? (() => undefined);
    this.#onClearResponse = options.onClearResponse ?? (() => undefined);
    this.#prepareHumanNoteResponse =
      options.prepareHumanNoteResponse ?? ((record) => (response) => options.onHumanNoteResponse?.(record, response));
    this.#onHumanNoteReconciled = options.onHumanNoteReconciled ?? (() => undefined);
  }

  async dispatchTargets(targetRefs: Iterable<string>): Promise<void> {
    await Promise.all([...new Set(targetRefs)].map((targetRef) => this.#dispatchTarget(targetRef)));
  }

  // Reopen unresolved records beside receipt reconciliation. A live snapshot
  // may omit accepted work, so dispatch preserves each original mutation ID
  // and payload for journal replay and instance-fence validation.
  async restoreProvenAbsent(targetRef: string, authoritativeIds: ReadonlySet<string>): Promise<void> {
    const restored = await this.#storage.restoreProvenAbsent(targetRef, authoritativeIds);
    if (restored.length > 0) this.#onStorageChange([targetRef]);
  }

  // A caller's own reading of this target's last-accepted intentSequence,
  // for stamping a QueueSnapshot's `cut` at the snapshot's true arrival - see
  // that field's own comment for why that has to happen before the caller's
  // own further async work, not inside reconcileQueueSnapshot itself. Never
  // advances it; only #recordAccepted (an actual accept) does that. Async
  // only because the first call for any target rehydrates from storage -
  // once rehydrated, every further reading (this target's or another's)
  // resolves off the same cached map.
  async peekAcceptCounter(targetRef: string): Promise<number> {
    await this.#ensureAcceptSequencesRehydrated();
    return this.#lastAcceptedIntentSequence.get(targetRef) ?? 0;
  }

  // One-time, whole-storage catch-up: a fresh dispatcher instance (a reload,
  // a second tab) starts with no memory of what a PREVIOUS instance already
  // accepted into this same durable storage, so its own peekAcceptCounter
  // would otherwise read 0 forever and protect every already-accepted record
  // as though it were newer than any snapshot's cut. listOptimistic's own
  // records are exactly this target's accepted, not-yet-reflected work
  // (MutationOptimisticRecord's `state` is always "accepted" by type), so
  // their own max intentSequence per target IS this target's last-accepted
  // value - no separate durable clock to read.
  #ensureAcceptSequencesRehydrated(): Promise<void> {
    if (!this.#acceptSequenceRehydration) {
      this.#acceptSequenceRehydration = this.#storage.listOptimistic().then((records) => {
        for (const record of records) this.#recordAccepted(record.targetRef, record.intentSequence);
      });
    }
    return this.#acceptSequenceRehydration;
  }

  #recordAccepted(targetRef: string, intentSequence: number): void {
    const current = this.#lastAcceptedIntentSequence.get(targetRef) ?? 0;
    if (intentSequence > current) this.#lastAcceptedIntentSequence.set(targetRef, intentSequence);
  }

  // The one entry point for a fresh queue reading, from either a live push,
  // a hydrate, or a thread/clear response (#1706's shape, #1717's Medium 3
  // added the third). An applied drain or a consumed queue intent leaves no
  // push naming the ids it consumed - queueChanged carries only what
  // remains, steering/injected only the drain's own id - so a same-client
  // queue intent accepted before it sits in the optimistic store unreflected
  // forever unless something retires it against a fresh reading of what the
  // queue holds now. Settles this snapshot's own named ids (the durable
  // copy's job is done once an authoritative feed shows the id directly)
  // and retires `turn/queue` records absent from it AND accepted before its
  // own `cut` - never guessing when `snapshot.ids` is undefined (unknown
  // coverage; see QueueSnapshot), and never trusting absence alone to prove
  // a record predates the snapshot (#1717's Medium 1). Serialized per
  // target with an accepted intent's own optimistic write
  // (#serializedWithQueueReconciliation) and gated on revision monotonicity
  // within one session instance (QueueSnapshot's own `instanceId`/`cut`
  // comments cover both).
  async reconcileQueueSnapshot(targetRef: string, snapshot: QueueSnapshot): Promise<string[]> {
    return this.#serializedWithQueueReconciliation(targetRef, () =>
      this.#reconcileQueueSnapshotNow(targetRef, snapshot),
    );
  }

  // Shared per-target chain: a queue snapshot's own retire-scan
  // (#reconcileQueueSnapshotNow) and an accepted turn/queue intent's own
  // optimistic write (#attempt, below) are two otherwise-independent async
  // paths for the same target - #1717's Medium 2 measured that a record
  // accepted between a snapshot's capture and the scan's read could be
  // retired as though it had never existed, since nothing serialized them
  // against each other. Routing both through this one chain means whichever
  // reaches it first runs to completion before the other's fn is even
  // called, so a scan can never observe a write that lands mid-scan.
  async #serializedWithQueueReconciliation<T>(targetRef: string, fn: () => Promise<T>): Promise<T> {
    const previous = this.#queueReconciliations.get(targetRef) ?? Promise.resolve();
    const chained = previous
      .catch(() => undefined)
      .then(fn)
      .finally(() => {
        if (this.#queueReconciliations.get(targetRef) === chained) this.#queueReconciliations.delete(targetRef);
      });
    this.#queueReconciliations.set(targetRef, chained);
    return chained;
  }

  async #reconcileQueueSnapshotNow(targetRef: string, snapshot: QueueSnapshot): Promise<string[]> {
    if (!snapshot.authoritative) return [];
    // The wire gives no ordering across instances (no sequence or generation
    // number on thread/queueChanged, a thread/read response, or a
    // thread/clear response spans a clear - that exists only for navigation
    // invalidation, a separate subsystem), so an already-superseded
    // instance's in-flight snapshot is rejected outright rather than
    // compared: its own revision means nothing here any more.
    if (this.#supersededQueueInstances.get(targetRef)?.has(snapshot.instanceId)) return [];
    let state = this.#queueState.get(targetRef);
    if (state === undefined || state.instance !== snapshot.instanceId) {
      // First sight of this instance for the target, from ANY event -
      // supersede whatever was current and reset the baseline BEFORE
      // deciding coverage, regardless of whether THIS snapshot's own ids
      // are known. Fixing this ordering matters: coverage-unknown snapshots
      // returning before this reset left the old instance uncondemned, so a
      // later delayed snapshot from it could still be accepted, flip the
      // state back, and (on the transition that eventually followed) mark
      // the real, live instance superseded instead (#1717's Medium 2).
      if (state !== undefined) {
        const superseded = this.#supersededQueueInstances.get(targetRef) ?? new Set<string>();
        superseded.add(state.instance);
        this.#supersededQueueInstances.set(targetRef, superseded);
      }
      state = { instance: snapshot.instanceId, revision: -1, observedRevision: -1, cut: snapshot.cut };
      this.#queueState.set(targetRef, state);
    }
    // A revision is only comparable within its own instance - see
    // QueueSnapshot's own comment on why a new instance's low revision is
    // never stale against an old instance's higher one. The reset above
    // already put this target on `snapshot.instanceId` with revision -1, so
    // a first-ever snapshot from a new instance always passes here.
    // `observedRevision` is checked with `<`, not `<=`: an unknown-coverage
    // snapshot fences out anything OLDER, but a known snapshot at the SAME
    // revision must still get to replace it (the row directly below).
    if (snapshot.revision <= state.revision || snapshot.revision < state.observedRevision) return [];
    if (snapshot.ids === undefined) {
      // Nothing to reconcile, but the gate above still needs to remember
      // this revision was SEEN - see `observedRevision`'s own comment.
      this.#queueState.set(targetRef, { ...state, observedRevision: snapshot.revision });
      return [];
    }
    await this.reconcileIdentities(snapshot.ids);
    const settled = await this.#storage.settleOptimisticAbsent(targetRef, "turn/queue", snapshot.ids, snapshot.cut);
    // Recorded only once every write above has succeeded: a failed
    // reconciliation must leave the state where it was so a retry at the
    // same revision is not discarded as stale.
    this.#queueState.set(targetRef, {
      instance: snapshot.instanceId,
      revision: snapshot.revision,
      observedRevision: snapshot.revision,
      cut: snapshot.cut,
    });
    if (settled.length > 0) this.#onStorageChange([targetRef]);
    return settled;
  }

  async reconcileIdentities(clientMutationIds: Iterable<string>): Promise<void> {
    const targetRefs = new Set<string>();
    await Promise.all(
      [...new Set(clientMutationIds)].map(async (clientMutationId) => {
        const record =
          (await this.#storage.getOutbox(clientMutationId)) ??
          (await this.#storage.getOptimistic(clientMutationId)) ??
          (await this.#storage.getRecovery(clientMutationId));
        if (record?.method === "notes/human/set") this.#onHumanNoteReconciled(record);
        if (await this.#storage.settleApplied(clientMutationId)) {
          if (record) targetRefs.add(record.targetRef);
        }
      }),
    );
    if (targetRefs.size > 0) this.#onStorageChange([...targetRefs]);
  }

  #dispatchTarget(targetRef: string): Promise<void> {
    this.#requestedRuns.set(targetRef, (this.#requestedRuns.get(targetRef) ?? 0) + 1);
    const existing = this.#dispatching.get(targetRef);
    if (existing) return existing;

    const dispatch = this.#runTarget(targetRef).finally(() => {
      if (this.#dispatching.get(targetRef) === dispatch) this.#dispatching.delete(targetRef);
    });
    this.#dispatching.set(targetRef, dispatch);
    return dispatch;
  }

  async #runTarget(targetRef: string): Promise<void> {
    let observedRun = 0;
    do {
      observedRun = this.#requestedRuns.get(targetRef) ?? 0;
      const shouldContinue = await this.#drainTarget(targetRef);
      if (!shouldContinue) return;
    } while (observedRun !== (this.#requestedRuns.get(targetRef) ?? 0));
  }

  async #drainTarget(targetRef: string): Promise<boolean> {
    for (;;) {
      const client = this.#getClient(targetRef);
      if (client?.state !== "ready") return false;
      const loaded = await this.#storage.nextDispatchable(targetRef);
      if (!loaded) return true;

      // Another tab may have settled or reclassified the record after this
      // tab's list read. Sending is allowed only after an extant-state recheck.
      const current = await this.#storage.getOutbox(loaded.clientMutationId);
      if (current?.state !== "submitting") continue;
      if (this.#getClient(targetRef) !== client) return false;

      if (!(await this.#storage.markAttempted(current.clientMutationId))) continue;
      // Keep the committed attempt evidence if this client was retired: another
      // tab may have dispatched the same record, so absence of this send is not
      // proof of non-delivery. Live recovery retries the original payload.
      if (this.#getClient(targetRef) !== client) return false;
      const outcome = await this.#attempt(client, current);
      if (outcome === "stop") return false;
    }
  }

  async #attempt(client: AppwireClientLike, record: MutationOutboxRecord): Promise<"advance" | "stop"> {
    try {
      const method = mutationMethod(record.method);
      const request = client.request as unknown as (
        requestMethod: MethodName,
        params: Record<string, unknown>,
      ) => Promise<unknown>;
      const applyHumanNoteResponse = method === "notes/human/set" ? this.#prepareHumanNoteResponse(record) : undefined;
      const result = await request.call(client, method, record.payload);
      const receipt = mutationReceipt(result);
      if (
        !receipt ||
        receipt.clientMutationId !== record.clientMutationId ||
        (receipt.disposition !== "applied" && receipt.disposition !== "replayed")
      ) {
        return "stop";
      }
      if (method === "thread/clear") {
        const response = clearResponse(result, record.targetRef);
        if (!response) return "stop";
        this.#onClearResponse(record.targetRef, response);
      }
      if (method === "notes/human/set") {
        if (!result || typeof result !== "object" || !("note" in result) || typeof result.note !== "string")
          return "stop";
        applyHumanNoteResponse?.({ note: result.note, receipt });
      }
      // Recorded here, before this write joins the shared per-target chain
      // (which may delay when it actually lands) - record.intentSequence is
      // already this target's own true accept order (allocated at enqueue,
      // never reused), for a later snapshot's cut to compare against
      // (QueueSnapshot's own comment; #1717's Medium 1).
      this.#recordAccepted(record.targetRef, record.intentSequence);
      // Serialized against any in-flight queue reconciliation for this
      // target (see #serializedWithQueueReconciliation): otherwise this
      // write and a concurrent snapshot's retire-scan could interleave.
      await this.#serializedWithQueueReconciliation(record.targetRef, () =>
        this.#storage.settleReceipt(record.clientMutationId, receipt.projectionState),
      );
      this.#onStorageChange([record.targetRef]);
      return "advance";
    } catch (error) {
      const data = mutationErrorData(error);
      if (data?.clientMutationId !== record.clientMutationId) {
        // A rejection that names a DIFFERENT mutation is not this record's to
        // judge. One that names none can still be terminal: the appwire
        // client correlates this rejection to THIS request, and an
        // invalid-params / invalid-request code means the server refused the
        // payload's shape without executing it (the hub validates before
        // forwarding, appwire.InvalidParams, which names no clientMutationId)
        // — an identical retry can never succeed. Retaining "submitting"
        // here turned one malformed intent at the FIFO head into a
        // permanently parked thread (kata wr3s). Recovery preserves the text
        // and surfaces the failure; the FIFO advances.
        if (
          data?.clientMutationId === undefined &&
          error instanceof WireError &&
          (error.code === JSONRPC_INVALID_PARAMS || error.code === JSONRPC_INVALID_REQUEST)
        ) {
          await this.#storage.transferToRecovery(record.clientMutationId, "rejected", rejectionReason(error, data));
          this.#onStorageChange([record.targetRef]);
          return "advance";
        }
        return "stop";
      }
      if (data?.mutationOutcome === "notAccepted") {
        await this.#storage.transferToRecovery(record.clientMutationId, "rejected", rejectionReason(error, data));
        this.#onStorageChange([record.targetRef]);
        return "advance";
      }
      if (data?.mutationOutcome === "targetDeleted") {
        await this.#storage.transferToRecovery(record.clientMutationId, "orphaned");
        this.#onStorageChange([record.targetRef]);
        return "advance";
      }
      if (
        data?.mutationOutcome === "unknown" &&
        (data.cause === "persistenceUnavailable" || data.retryDisposition === "blocked")
      ) {
        try {
          await this.#storage.markUnknown(record.clientMutationId, "blockedUnknown");
          this.#onStorageChange([record.targetRef]);
        } finally {
          this.#onBlockedMutation(record.targetRef, client);
        }
      }
      // Request timeouts, transport failures, and automatically retryable
      // unknown outcomes retain submitting. A later ready/discovery event
      // starts the next attempt; this loop never spins on an ambiguous result.
      return "stop";
    }
  }
}

// Wire values of appwire's CodeInvalidRequest / CodeInvalidParams
// (appwire/errors.go) — the standard JSON-RPC codes. Both mean the request
// was refused on shape alone, before execution, so they are deterministic:
// resending the identical payload can never produce a different answer.
const JSONRPC_INVALID_REQUEST = -32600;
const JSONRPC_INVALID_PARAMS = -32602;

const RETRY_SAFE_MUTATION_METHODS: ReadonlySet<string> = new Set([
  "turn/start",
  "turn/steer",
  "turn/interrupt",
  "turn/queue",
  "turn/drainAsSteer",
  "turn/promoteQueuedAsSteer",
  "turn/cancelQueued",
  "thread/clear",
  "notes/human/set",
]);

// rejectionReason extracts what to show a user whose control was refused.
// Preference order: the daemon's own structured explanation, then the wire
// error message, then nothing -- a row with no reason is still better than a
// row that claims a reason it does not have.
//
// This is the storage side of kata 2f41. A refused Steer or Stop rendered as a
// bare recovery row is indistinguishable from a queued one, which is how five
// rejections in a mutation journal produced nothing on screen.
function rejectionReason(error: unknown, data: ReturnType<typeof mutationErrorData>): string | undefined {
  if (error instanceof WireError) {
    // The message is the daemon's own sentence -- "turn is not active". Prefer
    // it over evenerErrorInfo, which is a CATEGORY (appwire.ErrorInfo:
    // "sessionUnavailable", "conflict"); showing the category tells the user
    // the class of failure instead of the failure.
    const message = error.message.trim();
    if (message) return message;
    const category = error.evenerErrorInfo?.trim();
    if (category) return category;
  }
  return data?.mutationOutcome;
}

function mutationMethod(method: string): MethodName {
  if (!RETRY_SAFE_MUTATION_METHODS.has(method)) throw new Error(`Unknown mutation method: ${method}`);
  return method as MethodName;
}

function mutationReceipt(result: unknown): MutationReceipt | undefined {
  if (!result || typeof result !== "object" || !("receipt" in result)) return undefined;
  const receipt = (result as { receipt?: unknown }).receipt;
  if (!receipt || typeof receipt !== "object") return undefined;
  const candidate = receipt as Partial<MutationReceipt>;
  if (
    typeof candidate.clientMutationId !== "string" ||
    typeof candidate.disposition !== "string" ||
    typeof candidate.threadId !== "string" ||
    typeof candidate.projectionState !== "string"
  ) {
    return undefined;
  }
  return candidate as MutationReceipt;
}

function clearResponse(result: unknown, targetRef: string): ThreadClearResponse | undefined {
  if (!result || typeof result !== "object") return undefined;
  const candidate = result as Partial<ThreadClearResponse>;
  if (candidate.ref !== targetRef || typeof candidate.ref !== "string") return undefined;
  if (!candidate.thread || typeof candidate.thread !== "object") return undefined;
  const thread = candidate.thread;
  if (typeof thread.id !== "string" || thread.id === "") return undefined;
  if (!thread.evener || typeof thread.evener !== "object") return undefined;
  if (thread.evener.ref !== targetRef) return undefined;
  return candidate as ThreadClearResponse;
}
