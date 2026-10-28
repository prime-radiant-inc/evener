// Dispatching a durable mutation: one at a time per thread, and never a blind
// replay.
//
// A record is already committed to storage before this class sees it, so the
// question here is not "did the user ask for this" but "has the hub applied it
// yet, and if the answer is unknown, what may this client do next". The rules
// that answer it are the same on every host and are what this module carries:
// serialized dispatch per target ref (a second submission for the same thread
// waits rather than racing), a receipt reconciled into storage before the next
// attempt, a terminal refusal turned into a recovery record with the daemon's
// own reason, a transient backpressure refusal kept queued for a later event
// (#4342), and an outcome nobody can vouch for left blockedUnknown — never
// retried until an authoritative read has settled it (#1116's standing
// decision).
//
// Everything host-shaped is a parameter: the storage is D26's
// MutationOutboxStorage port, and the surfaces that react to an outcome
// (toasts, a clear's response, a shared note's authority) are callbacks the
// app supplies. No clock, no timer, no DOM.
import type { AppwireClientLike } from "../../clientLike";
import { mutationErrorData, refusedBeforeRunning, WireError } from "../../errors";
import {
  RETRY_SAFE_MUTATION_METHODS as GENERATED_RETRY_SAFE_MUTATION_METHODS,
  type MethodName,
  type MutationReceipt,
  type NotesHumanSetResponse,
  type ThreadClearResponse,
} from "../../types.gen";
import { isClientReady, type MutationOutboxStorage } from "./outbox";
import type { MutationAttachmentRef, MutationOutboxRecord, MutationRecord } from "./records";

// The dispatcher always supplies a target ref, so its lookup requires one.
// That is deliberately narrower than the outbox's ref-less MutationClientLookup
// (which asks for the runtime's current client): keeping the parameter required
// preserves assignability for a consumer that already implements
// `(targetRef: string) => ...`.
//
// `method` names the record the dispatcher is about to send for `targetRef`, so
// a host can fence a queued verb the daemon would refuse while admitting the
// others (the hub's recovery admission carves only turn/start out of the
// resume-only fence). Optional, so a lookup that answers for the ref alone
// (`(targetRef: string) => ...`) stays assignable and keeps its previous
// answer.
export type MutationDispatchClientLookup = (targetRef: string, method?: string) => AppwireClientLike | null | undefined;

// Why a storage change notification fired. The transient refusal's rollback
// reverted only attempt evidence - fields no queue projection carries - so
// the projected queue is unchanged (`projectedShapeUnchanged`); a fence-held
// rollback canceled the row, which every projection renders, and a row
// settled out from under the attempt is gone from view (both changed). A
// host whose projection refresh would read an identical queue can skip it;
// native's consumer re-reads regardless, keeping the round-6 contract that
// the transient branch publishes like every other outcome.
export interface MutationStorageChangeCause {
  kind: "transientRefusal";
  projectedShapeUnchanged: boolean;
}

export interface MutationDispatcherOptions<A extends MutationAttachmentRef = MutationAttachmentRef> {
  getClient: MutationDispatchClientLookup;
  onStorageChange?: (targetRefs: string[], cause?: MutationStorageChangeCause) => void;
  onBlockedMutation?: (targetRef: string, client: AppwireClientLike) => void;
  // #4342: fired when the hub refused an attempt in band as transient
  // backpressure (notAccepted plus an automatic retry disposition) — the
  // record stays queued and this callback owns surfacing the busy state and
  // pacing the next attempt. The error is the hub's own refusal, so a host
  // can show its message; the dispatcher itself stops the loop rather than
  // spinning on the refusal.
  onTransientRefusal?: (record: MutationOutboxRecord<A>, error: WireError) => void;
  // May be async: the clear's model publication owns the response's side
  // effects (including any best-effort cleanup the host awaits before
  // publishing), and the attempt does not settle the clear's own record
  // until that has happened.
  onClearResponse?: (targetRef: string, response: ThreadClearResponse) => void | Promise<void>;
  // Capture response authority immediately before each transport attempt,
  // after any reconnect hydration, rather than once at durable enqueue.
  prepareHumanNoteResponse?: (record: MutationOutboxRecord<A>) => (response: NotesHumanSetResponse) => void;
  onHumanNoteReconciled?: (record: MutationRecord<A>) => void;
}

export class MutationDispatcher<A extends MutationAttachmentRef = MutationAttachmentRef> {
  readonly #storage: MutationOutboxStorage<A>;
  readonly #getClient: MutationDispatcherOptions<A>["getClient"];
  readonly #onStorageChange: NonNullable<MutationDispatcherOptions<A>["onStorageChange"]>;
  readonly #onBlockedMutation: NonNullable<MutationDispatcherOptions<A>["onBlockedMutation"]>;
  readonly #onTransientRefusal: NonNullable<MutationDispatcherOptions<A>["onTransientRefusal"]>;
  readonly #onClearResponse: NonNullable<MutationDispatcherOptions<A>["onClearResponse"]>;
  readonly #prepareHumanNoteResponse: NonNullable<MutationDispatcherOptions<A>["prepareHumanNoteResponse"]>;
  readonly #onHumanNoteReconciled: NonNullable<MutationDispatcherOptions<A>["onHumanNoteReconciled"]>;
  readonly #dispatching = new Map<string, Promise<void>>();
  readonly #requestedRuns = new Map<string, number>();

  constructor(storage: MutationOutboxStorage<A>, options: MutationDispatcherOptions<A>) {
    this.#storage = storage;
    this.#getClient = options.getClient;
    this.#onStorageChange = options.onStorageChange ?? (() => undefined);
    this.#onBlockedMutation = options.onBlockedMutation ?? (() => undefined);
    this.#onTransientRefusal = options.onTransientRefusal ?? (() => undefined);
    this.#onClearResponse = options.onClearResponse ?? (() => undefined);
    this.#prepareHumanNoteResponse = options.prepareHumanNoteResponse ?? (() => () => undefined);
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
      if (!isClientReady(client)) return false;
      const loaded = await this.#storage.nextDispatchable(targetRef);
      if (!loaded) return true;

      // Another tab may have settled or reclassified the record after this
      // tab's list read. Sending is allowed only after an extant-state recheck.
      const current = await this.#storage.getOutbox(loaded.clientMutationId);
      if (current?.state !== "submitting") continue;
      // The method-aware recheck: the top-of-loop lookup answered for the ref
      // alone (it runs before nextDispatchable picks a record), so a host that
      // fences one queued verb must still be able to park THIS record here.
      if (this.#getClient(targetRef, current.method) !== client) return false;

      if (!(await this.#storage.markAttempted(current.clientMutationId))) continue;
      // Keep the committed attempt evidence if this client was retired: another
      // tab may have dispatched the same record, so absence of this send is not
      // proof of non-delivery. Live recovery retries the original payload.
      if (this.#getClient(targetRef, current.method) !== client) return false;
      const outcome = await this.#attempt(client, current);
      if (outcome === "stop") return false;
    }
  }

  async #attempt(client: AppwireClientLike, record: MutationOutboxRecord<A>): Promise<"advance" | "stop"> {
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
        await this.#onClearResponse(record.targetRef, response);
      }
      if (method === "notes/human/set") {
        if (!result || typeof result !== "object" || !("note" in result) || typeof result.note !== "string")
          return "stop";
        applyHumanNoteResponse?.({ note: result.note, receipt });
      }
      // A drain's own receipt names the queue intents it consumed (durable
      // across a replay, unlike a live push): settle them the same way any
      // other authoritative feed does, through reconcileIdentities -- BEFORE
      // the drain's own record settles. A failure here (a transient
      // IndexedDB error, say) then throws out of this whole attempt with the
      // drain record still "submitting": the next dispatch resends it, the
      // server replays it, and the replay's own receipt carries the same
      // consumed ids again. Settling the drain first would durably remove
      // the one record whose receipt names them, with no path left to retry.
      // Scoped to method === "turn/drainAsSteer": the field means "consumed
      // by THIS drain," never "consumed by whatever this receipt happens to
      // be for" -- a well-formed array riding a different mutation's receipt
      // must settle nothing.
      if (method === "turn/drainAsSteer") {
        const consumed = validConsumedClientMutationIds(receipt.consumedClientMutationIds);
        if (consumed.length > 0) await this.reconcileIdentities(consumed);
      }
      await this.#storage.settleReceipt(record.clientMutationId, receipt.projectionState);
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
        if (data?.clientMutationId === undefined && refusedBeforeRunning(error)) {
          await this.#storage.transferToRecovery(record.clientMutationId, "rejected", rejectionReason(error, data));
          this.#onStorageChange([record.targetRef]);
          return "advance";
        }
        return "stop";
      }
      if (data?.mutationOutcome === "notAccepted" && data.retryDisposition === "automatic") {
        // #4342: the hub refused the attempt in band without queueing it —
        // this attempt never reached the queue, so it stays rolled back in
        // the FIFO while any earlier or concurrent unresolved attempt's
        // evidence survives. The busy surface and the pacing arm only when
        // the row comes back to its never-attempted dispatchable shape (so
        // a reconnect reconcile's onlyAttempted-guarded park skips it and
        // the ordinary flush rediscovers it): a refusal that leaves
        // residual evidence — or finds the row already parked or settled
        // elsewhere — hands the row to its ordinary uncertain treatment
        // instead. This loop stops rather than spinning on the refusal.
        // mutationErrorData answers only WireErrors, so the data this branch
        // matched on proves the error's shape.
        // The row's pre-rollback state tells the true shapes apart: the
        // rollback's `true` covers both a clean submitting rollback (the
        // projected queue unchanged) and a blockedUnknown reopen (the
        // reconcile parked the row while its request was pending, and the
        // refusal's proof of non-delivery moves it back where every
        // projection renders it). A concurrent park in the read-to-rollback
        // window misclassifies once, and the next notification or
        // discovery refresh self-heals - the same bounded cross-tab
        // interleaving the attempt counts already tolerate.
        const beforeRollback = await this.#storage.getOutbox(record.clientMutationId);
        const rolledBackClean = await this.#storage.unmarkAttempted(record.clientMutationId);
        if (rolledBackClean) {
          this.#onTransientRefusal(record, error as WireError);
        }
        // The false outcomes split: a fence-held rollback canceled the row
        // (every projection renders the state change) while residual
        // evidence or a concurrent settlement left no projected change at
        // all. One read classifies - the refusal is a saturation-time
        // event, so the read costs nothing on the healthy path. The
        // rollback can still move the row where projections see it, and
        // hosts whose projections refresh through storage notifications
        // have no other observer for those transitions.
        let projectedShapeUnchanged = rolledBackClean && beforeRollback?.state === "submitting";
        if (!rolledBackClean) {
          const row = await this.#storage.getOutbox(record.clientMutationId);
          projectedShapeUnchanged = row !== undefined && row.state !== "canceled";
        }
        this.#onStorageChange([record.targetRef], { kind: "transientRefusal", projectedShapeUnchanged });
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

// The retry-safe methods the dispatcher can settle. urls/remove is retry-safe
// on the wire, but its response carries no mutation receipt, so a queued
// record for it could never settle; its callers resend it directly.
export const RETRY_SAFE_MUTATION_METHODS: ReadonlySet<string> = new Set(
  GENERATED_RETRY_SAFE_MUTATION_METHODS.filter((method) => method !== "urls/remove"),
);

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

// validConsumedClientMutationIds treats anything that is not an array of
// non-empty strings as though the field were absent, never guessing. A
// string is itself iterable character-by-character, so an unguarded spread
// of a malformed value would silently settle records named by coincidence
// rather than by the daemon.
export function validConsumedClientMutationIds(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.every((id): id is string => typeof id === "string" && id.trim() !== "") ? value : [];
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
