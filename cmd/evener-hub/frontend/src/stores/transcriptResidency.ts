import type { AnyNotification } from "@evener/appwire-client";
import type { BackgroundTranscriptOptions } from "./backgroundTranscriptOptions";
import { TranscriptPayloadBudget } from "./transcriptPayloadBudget";

export interface ReceivedTranscriptUpdate {
  sequence: number;
  now: number;
  notification: AnyNotification;
  bytes: number;
}

// The reduced residency port. The 6d removal deleted the legacy recorded
// fold/rewrite coordinator; what survives is hydration scheduling and the
// lifecycle (connection, Clear, disposal). A caller may supply its own run; the
// port's `hydrate` is the default when it does not.
export interface TranscriptResidencyPort {
  hydrate?(ref: string, intent: "background" | "foreground" | "final", signal: AbortSignal): Promise<void>;
}

interface HydrationTask {
  ref: string;
  intent: "background" | "foreground";
  controller: AbortController;
  run(signal: AbortSignal): Promise<void>;
  promise: Promise<void>;
  resolve(): void;
  reject(error: unknown): void;
  queued: boolean;
  started: boolean;
  settled: boolean;
  ownerSettled: boolean;
  retired: boolean;
  successor?: HydrationTask;
}

export interface TranscriptResidency {
  removed(ref: string): void;
  connection(generation: number, ready: boolean): void;
  reservePending(ref: string, bytes: number): boolean;
  releasePending(ref: string, bytes: number): void;
  scheduleHydration(
    ref: string,
    intent: "background" | "foreground",
    run?: (signal: AbortSignal) => Promise<void>,
    demand?: "join" | "replace",
  ): Promise<void>;
  revoke(ref: string, reason: "gap" | "storage" | "alias" | "deleted"): void;
  /** Synchronously retire in-flight hydration publication claims for a same-tab
   * clear before its transaction resolves. Issued requests keep their capacity
   * slot; only the logical claim to publish is retired. */
  retireClaims(): void;
  /** Release the synchronous claim fence after an aborted clear. */
  releaseClearClaim(): void;
  clear(epoch: number): void;
  dispose(): void;
  settled(): Promise<void>;
  snapshot(): TranscriptResidencySnapshot;
}

export interface TranscriptResidencySnapshot {
  residentBytes: number;
  pendingBytes: number;
  workerBytes: number;
  diskWorkers: number;
  networkHydrations: number;
  queuedHydrationRefs: readonly string[];
  committedWrites: number;
  refs: ReadonlyMap<
    string,
    {
      resident: boolean;
      residentBytes: number;
      handoffBytes: number;
      current: boolean;
      appliedSequence: number;
      persistence: "enabled" | "repairing" | "clear-suppressed";
      pending: readonly ReceivedTranscriptUpdate[];
      inFlight: readonly ReceivedTranscriptUpdate[];
    }
  >;
}

export function createTranscriptResidency(
  port: TranscriptResidencyPort,
  options: Readonly<BackgroundTranscriptOptions>,
): TranscriptResidency {
  const pendingBudget = new TranscriptPayloadBudget(options.pendingBytes);
  const hydrationQueue: HydrationTask[] = [];
  const hydrationByRef = new Map<string, HydrationTask>();
  const hydrationTasks = new Set<HydrationTask>();
  let connectionGeneration = 0;
  let ready = false;
  let networkHydrations = 0;
  let disposed = false;
  let retiringHydrations = false;
  const clearClaimRetiredRefs = new Map<string, "background" | "foreground">();

  function settleHydration(task: HydrationTask, error?: unknown): void {
    if (task.settled) return;
    task.settled = true;
    if (task.started) networkHydrations -= 1;
    hydrationTasks.delete(task);
    if (hydrationByRef.get(task.ref) === task) hydrationByRef.delete(task.ref);
    const queuedIndex = hydrationQueue.indexOf(task);
    if (queuedIndex >= 0) hydrationQueue.splice(queuedIndex, 1);
    const successor = task.successor;
    task.successor = undefined;
    if (successor && !successor.retired) {
      hydrationByRef.set(task.ref, successor);
      enqueueHydration(successor);
    }
    if (!task.ownerSettled) {
      task.ownerSettled = true;
      if (error === undefined) task.resolve();
      else task.reject(error);
    }
    startAvailableHydrations();
  }

  function enqueueHydration(task: HydrationTask): void {
    task.queued = true;
    hydrationQueue.push(task);
    hydrationQueue.sort((a, b) => Number(b.intent === "foreground") - Number(a.intent === "foreground"));
  }

  function startAvailableHydrations(): void {
    while (
      ready &&
      !disposed &&
      !retiringHydrations &&
      networkHydrations < options.networkHydrations &&
      hydrationQueue.length > 0
    ) {
      const next = hydrationQueue.shift();
      if (next && !next.settled && !next.retired && hydrationByRef.get(next.ref) === next) startHydration(next);
    }
  }

  function startHydration(task: HydrationTask): void {
    if (task.settled) return;
    if (task.retired || task.controller.signal.aborted || disposed || !ready) {
      settleHydration(task);
      return;
    }
    task.queued = false;
    task.started = true;
    networkHydrations += 1;
    void task.run(task.controller.signal).then(
      () => settleHydration(task),
      (error: unknown) => settleHydration(task, error),
    );
  }

  function retireHydration(task: HydrationTask): void {
    if (task.settled || task.retired) return;
    task.retired = true;
    if (task.successor) retireHydration(task.successor);
    task.successor = undefined;
    task.controller.abort();
    if (hydrationByRef.get(task.ref) === task) hydrationByRef.delete(task.ref);
    const queuedIndex = hydrationQueue.indexOf(task);
    if (queuedIndex >= 0) hydrationQueue.splice(queuedIndex, 1);
    if (!task.started) {
      settleHydration(task);
      return;
    }
    // Retire the useful owner-facing wait now, but keep an issued request's
    // network slot until the real task promise settles by response, rejection
    // or the client's existing deadline.
    if (!task.ownerSettled) {
      task.ownerSettled = true;
      task.resolve();
    }
  }

  function retireHydrations(): void {
    retiringHydrations = true;
    try {
      for (const task of [...hydrationTasks]) retireHydration(task);
    } finally {
      retiringHydrations = false;
      startAvailableHydrations();
    }
  }

  function retireHydrationForRef(ref: string): void {
    const task = hydrationByRef.get(ref);
    if (task) retireHydration(task);
  }

  function scheduleHydration(
    ref: string,
    intent: "background" | "foreground",
    run?: (signal: AbortSignal) => Promise<void>,
    demand: "join" | "replace" = "join",
  ): Promise<void> {
    if (!ready || disposed) return Promise.resolve();
    const existing = hydrationByRef.get(ref);
    const joined = demand === "replace" ? existing?.successor : existing;
    if (joined) {
      if (!joined.started && intent === "foreground") {
        joined.intent = "foreground";
        hydrationQueue.sort((a, b) => Number(b.intent === "foreground") - Number(a.intent === "foreground"));
      }
      return joined.promise;
    }
    let resolve!: () => void;
    let reject!: (error: unknown) => void;
    const promise = new Promise<void>((complete, fail) => {
      resolve = complete;
      reject = fail;
    });
    const controller = new AbortController();
    const task: HydrationTask = {
      ref,
      intent,
      controller,
      run: run ?? ((signal) => port.hydrate?.(ref, intent, signal) ?? Promise.resolve()),
      promise,
      resolve,
      reject,
      queued: false,
      started: false,
      settled: false,
      ownerSettled: false,
      retired: false,
    };
    hydrationTasks.add(task);
    // A replacement cannot use the predecessor's result. Coalesce it into one
    // successor, admitted through the capacity queue only after settlement.
    // Until then, independent refs can still use every genuinely free slot.
    if (existing) existing.successor = task;
    else {
      hydrationByRef.set(ref, task);
      enqueueHydration(task);
      startAvailableHydrations();
    }
    return promise;
  }

  const residency: TranscriptResidency = {
    removed(ref) {
      retireHydrationForRef(ref);
    },
    connection(generation, connected) {
      if (generation !== connectionGeneration || connected !== ready) {
        const generationChanged = generation !== connectionGeneration;
        connectionGeneration = generation;
        ready = connected;
        if (generationChanged || !connected) retireHydrations();
      }
    },
    reservePending(_ref, bytes) {
      return pendingBudget.reserve(bytes);
    },
    releasePending(_ref, bytes) {
      pendingBudget.release(bytes);
    },
    scheduleHydration,
    revoke(ref, _reason) {
      retireHydrationForRef(ref);
    },
    retireClaims() {
      for (const task of [...hydrationTasks]) {
        clearClaimRetiredRefs.set(task.ref, task.intent);
      }
      retireHydrations();
    },
    releaseClearClaim() {
      const refs = [...clearClaimRetiredRefs];
      clearClaimRetiredRefs.clear();
      for (const [ref, intent] of refs) {
        if (!ready || disposed) continue;
        // A captured page/end callback owns retired request state. Ask the port
        // for a fresh read instead, and consume any owner/transport rejection.
        void residency.scheduleHydration(ref, intent).catch(() => {});
      }
    },
    clear(_epoch) {
      clearClaimRetiredRefs.clear();
      retireHydrations();
    },
    dispose() {
      disposed = true;
      clearClaimRetiredRefs.clear();
      retireHydrations();
    },
    async settled() {
      // The reduced residency owns no disk work. It must never await an issued
      // network hydration: a caller's read may legitimately never settle (a
      // never-resolving refresh), exactly as the original, which waited on the
      // disk worker and never on networkHydrations.
      return;
    },
    snapshot() {
      return {
        residentBytes: 0,
        pendingBytes: pendingBudget.used,
        workerBytes: 0,
        diskWorkers: 0,
        networkHydrations,
        queuedHydrationRefs: hydrationQueue.map((task) => task.ref),
        committedWrites: 0,
        refs: new Map(),
      };
    },
  };
  return residency;
}
