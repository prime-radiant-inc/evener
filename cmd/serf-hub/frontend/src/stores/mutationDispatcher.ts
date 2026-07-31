import { mutationErrorData, WireError } from "../protocol/errors";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type { MethodName, MutationReceipt } from "../protocol/types.gen";
import type { MutationOutboxRecord } from "./mutationOutbox";
import type { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

export interface MutationDispatcherOptions {
  getClient: (targetRef: string) => AppwireClientLike | null | undefined;
  onStorageChange?: (targetRefs: string[]) => void;
}

export class MutationDispatcher {
  readonly #storage: MutationOutboxIndexedDB;
  readonly #getClient: MutationDispatcherOptions["getClient"];
  readonly #onStorageChange: NonNullable<MutationDispatcherOptions["onStorageChange"]>;
  readonly #dispatching = new Map<string, Promise<void>>();
  readonly #requestedRuns = new Map<string, number>();

  constructor(storage: MutationOutboxIndexedDB, options: MutationDispatcherOptions) {
    this.#storage = storage;
    this.#getClient = options.getClient;
    this.#onStorageChange = options.onStorageChange ?? (() => undefined);
  }

  async dispatchTargets(targetRefs: Iterable<string>): Promise<void> {
    await Promise.all([...new Set(targetRefs)].map((targetRef) => this.#dispatchTarget(targetRef)));
  }

  // restoreProvenAbsent reopens dispatch for blockedUnknown records the
  // authoritative read proved the daemon never accepted (see the storage
  // method's comment for why absence is proof). Runs beside
  // reconcileIdentities on the hydration publish path: reconcile settles what
  // the authority knows, this restores what it provably does not.
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
      const result = await request.call(client, method, record.payload);
      const receipt = mutationReceipt(result);
      if (
        !receipt ||
        receipt.clientMutationId !== record.clientMutationId ||
        (receipt.disposition !== "applied" && receipt.disposition !== "replayed")
      ) {
        return "stop";
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
        if (
          data?.clientMutationId === undefined &&
          error instanceof WireError &&
          (error.code === JSONRPC_INVALID_PARAMS || error.code === JSONRPC_INVALID_REQUEST)
        ) {
          await this.#storage.transferToRecovery(record.clientMutationId, "rejected");
          this.#onStorageChange([record.targetRef]);
          return "advance";
        }
        return "stop";
      }
      if (data?.mutationOutcome === "notAccepted") {
        await this.#storage.transferToRecovery(record.clientMutationId, "rejected");
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
        await this.#storage.markUnknown(record.clientMutationId, "blockedUnknown");
        this.#onStorageChange([record.targetRef]);
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
]);

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
