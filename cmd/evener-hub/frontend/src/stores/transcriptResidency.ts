import type { AnyNotification } from "@evener/appwire-client";
import {
  type CurrentTranscriptAdmission,
  currentTranscriptState,
  type ThreadModel,
  threadModelFromCurrentState,
} from "@evener/appwire-client";
import type { BackgroundTranscriptOptions } from "./backgroundTranscriptOptions";
import { createSecureUUID } from "./secureUUID";
import type { CurrentSessionRecord, SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { encodedPayloadBytes, TranscriptPayloadBudget } from "./transcriptPayloadBudget";

export interface TranscriptOwners {
  foreground: number;
  history: number;
  mutationPinned: boolean;
  background: number;
}

export interface ReceivedTranscriptUpdate {
  sequence: number;
  now: number;
  notification: AnyNotification;
  bytes: number;
}

export interface TranscriptResidencyPort {
  cache: SessionCacheIndexedDB;
  resident(ref: string): ThreadModel | undefined;
  publish(ref: string, model: ThreadModel): void;
  unload(ref: string): void | Promise<void>;
  admission(ref: string): CurrentTranscriptAdmission | undefined;
  fold(ref: string, model: ThreadModel, update: ReceivedTranscriptUpdate): ThreadModel;
  hydrate(ref: string, intent: "background" | "foreground" | "final", signal: AbortSignal): Promise<void>;
  writeTimers?(): { debounceMs: number; maxWaitMs: number };
  /** The shared paced-retry scheduler. When absent, repair runs immediately
   * (the reducer-level tests' behavior); the store wires it so real repairs use
   * one coalesced bounded-backoff timer per ref. */
  retryScheduler?(): ((attempt: number, retry: () => void) => () => void) | undefined;
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
}

export interface TranscriptResidency {
  owners(ref: string, owners: TranscriptOwners): void;
  removed(ref: string): void;
  connection(generation: number, ready: boolean): void;
  accept(ref: string, update: ReceivedTranscriptUpdate): boolean;
  published(ref: string, model: ThreadModel, appliedSequence: number, authoritative?: boolean): boolean;
  reservePending(ref: string, bytes: number): boolean;
  releasePending(ref: string, bytes: number): void;
  promote(ref: string): Promise<ThreadModel | undefined>;
  scheduleHydration(
    ref: string,
    intent: "background" | "foreground",
    run?: (signal: AbortSignal) => Promise<void>,
  ): Promise<void>;
  drain(ref: string): Promise<void>;
  revoke(ref: string, reason: "gap" | "storage" | "alias" | "deleted"): void;
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

type Persistence = "enabled" | "repairing" | "clear-suppressed";

// The exact bodies a worker is about to hand to the cache, sampled
// synchronously immediately before putCurrent: the starting resident body it
// read, the folded worker model, the record it is writing, and the captured
// in-flight batch. This is a restorable test-only seam for proving worker
// checkpoint coverage without a global spare charge; production retains no
// history and reads nothing from it.
export interface WorkerCheckpointObservation {
  ref: string;
  resident: ThreadModel | undefined;
  model: ThreadModel;
  record: CurrentSessionRecord;
  batch: readonly ReceivedTranscriptUpdate[];
}

let workerCheckpointObserverForTests: ((observation: WorkerCheckpointObservation) => void) | undefined;

export function observeWorkerCheckpointsForTests(
  observer: ((observation: WorkerCheckpointObservation) => void) | undefined,
): () => void {
  const previous = workerCheckpointObserverForTests;
  workerCheckpointObserverForTests = observer;
  return () => {
    workerCheckpointObserverForTests = previous;
  };
}

interface Entry {
  owners: TranscriptOwners;
  ownershipGeneration: number;
  checkpointOwnershipGeneration: number;
  appliedSequence: number;
  checkpointSequence: number;
  residentBytes: number;
  residentPostCutBytes: number;
  handoffBytes: number;
  pending: ReceivedTranscriptUpdate[];
  current: boolean;
  needsRecovery: boolean;
  evictAfterCommit: boolean;
  persistence: Persistence;
  // Clear suppression is lifted only by a qualifying explicit foreground
  // acquisition. `clearSuppressionHoldsPreClearHolders` stays true while any
  // foreground/history/pin holder that existed when clear committed remains:
  // no acquisition may capture a new lifetime until those holders release.
  // `freshForegroundLifetime` then marks the acquisition that began after they
  // did, whose authoritative hydration may restore persistence.
  clearSuppressionHoldsPreClearHolders: boolean;
  freshForegroundLifetime: boolean;
  lastForegroundUse: number;
  promoting: boolean;
  workSequence: number;
  inFlight: ReceivedTranscriptUpdate[];
  // One coalesced storage/overload repair timer per ref. An expired callback or
  // a superseded repair owns nothing: it acts only while it is still this
  // entry's current record.
  repair?: { cancel: () => void; attempt: number };
  repairAttempt: number;
  flushTimer?: ReturnType<typeof setTimeout>;
  maxWaitTimer?: ReturnType<typeof setTimeout>;
  work?: Promise<boolean>;
  waiters: Array<() => void>;
}

export function createTranscriptResidency(
  port: TranscriptResidencyPort,
  options: Readonly<BackgroundTranscriptOptions>,
): TranscriptResidency {
  const ownerToken = createSecureUUID();
  const entries = new Map<string, Entry>();
  const pendingBudget = new TranscriptPayloadBudget(options.pendingBytes);
  const residentBudget = new TranscriptPayloadBudget(options.residentBytes);
  const workQueue: string[] = [];
  const urgentQueue: string[] = [];
  const hydrationQueue: HydrationTask[] = [];
  const hydrationByRef = new Map<string, HydrationTask>();
  const hydrationTasks = new Set<HydrationTask>();
  let connectionGeneration = 0;
  let ready = false;
  let clearEpoch = 0;
  let workerBytes = 0;
  let diskWorkers = 0;
  let networkHydrations = 0;
  let committedWrites = 0;
  let disposed = false;
  let retiringHydrations = false;
  let drainPromise: Promise<void> | undefined;
  // Post-promotion durable-epoch rechecks: each is tracked asynchronous work
  // of the promotion that scheduled it, not a global lock. settled() joins
  // them so a test (or a caller) can observe the fenced recovery they trigger.
  const epochRechecks = new Set<Promise<void>>();

  const entryFor = (ref: string): Entry => {
    const entry = entries.get(ref);
    if (entry) return entry;
    const created: Entry = {
      owners: { foreground: 0, history: 0, mutationPinned: false, background: 0 },
      ownershipGeneration: 0,
      checkpointOwnershipGeneration: -1,
      appliedSequence: 0,
      checkpointSequence: 0,
      residentBytes: 0,
      residentPostCutBytes: 0,
      handoffBytes: 0,
      pending: [],
      current: false,
      needsRecovery: false,
      evictAfterCommit: false,
      persistence: "enabled",
      clearSuppressionHoldsPreClearHolders: false,
      freshForegroundLifetime: false,
      lastForegroundUse: 0,
      promoting: false,
      workSequence: 0,
      inFlight: [],
      repairAttempt: 0,
      waiters: [],
    };
    entries.set(ref, created);
    return created;
  };

  const held = (entry: Entry): boolean =>
    entry.owners.foreground > 0 || entry.owners.history > 0 || entry.owners.mutationPinned;
  const owned = (entry: Entry): boolean => held(entry) || entry.owners.background > 0;

  function releaseEntryPending(entry: Entry): void {
    const bytes = entry.pending.reduce((sum, update) => sum + update.bytes, 0);
    if (bytes > 0) pendingBudget.release(bytes);
    entry.pending = [];
  }

  function enqueue(ref: string, urgent = false): void {
    const entry = entries.get(ref);
    if (entry?.flushTimer) {
      clearTimeout(entry.flushTimer);
      entry.flushTimer = undefined;
    }
    if (entry?.maxWaitTimer) {
      clearTimeout(entry.maxWaitTimer);
      entry.maxWaitTimer = undefined;
    }
    if (entry?.work) return;
    if (urgent) {
      const normalIndex = workQueue.indexOf(ref);
      if (normalIndex >= 0) workQueue.splice(normalIndex, 1);
      if (!urgentQueue.includes(ref)) urgentQueue.push(ref);
      void runWorker();
      return;
    }
    if (urgentQueue.includes(ref)) return;
    if (!workQueue.includes(ref)) workQueue.push(ref);
    void runWorker();
  }

  function scheduleResidentWrite(ref: string, entry: Entry): void {
    if (entry.work) return;
    const timers = port.writeTimers?.() ?? { debounceMs: 1_000, maxWaitMs: 5_000 };
    if (entry.flushTimer) clearTimeout(entry.flushTimer);
    else entry.maxWaitTimer = setTimeout(() => fireResidentWrite(ref, entry), timers.maxWaitMs);
    entry.flushTimer = setTimeout(() => {
      fireResidentWrite(ref, entry);
    }, timers.debounceMs);
  }

  function fireResidentWrite(ref: string, entry: Entry): void {
    if (entry.flushTimer) clearTimeout(entry.flushTimer);
    if (entry.maxWaitTimer) clearTimeout(entry.maxWaitTimer);
    entry.flushTimer = undefined;
    entry.maxWaitTimer = undefined;
    enqueue(ref);
  }

  async function waitForEntryCut(ref: string, entry: Entry, sequence: number): Promise<boolean> {
    while (entry.checkpointSequence < sequence) {
      if (entry.persistence !== "enabled") return false;
      if (!entry.work && (!urgentQueue.includes(ref) || workQueue.includes(ref))) enqueue(ref, true);
      if (entry.work) {
        await entry.work.catch(() => false);
      } else {
        await new Promise<void>((resolve) => entry.waiters.push(resolve));
      }
    }
    return entry.persistence === "enabled" && entry.checkpointSequence >= sequence;
  }

  function enqueueLeastRecentlyUsedVictims(exceptRef: string, incomingBytes: number): void {
    let bytesToFree = residentBudget.used + incomingBytes - residentBudget.limit;
    if (bytesToFree <= 0) return;
    const candidates = [...entries]
      .filter(
        ([ref, entry]) =>
          ref !== exceptRef &&
          entry.residentBytes > 0 &&
          !held(entry) &&
          entry.owners.background > 0 &&
          entry.persistence === "enabled" &&
          !entry.work,
      )
      .sort((a, b) => a[1].lastForegroundUse - b[1].lastForegroundUse);
    for (const [ref, entry] of candidates) {
      entry.evictAfterCommit = true;
      bytesToFree -= entry.residentBytes;
      enqueue(ref);
      if (bytesToFree <= 0) break;
    }
  }

  function resolveWaiters(entry: Entry): void {
    for (const resolve of entry.waiters.splice(0)) resolve();
  }

  function cancelRepair(entry: Entry): void {
    const record = entry.repair;
    entry.repair = undefined;
    entry.repairAttempt = 0;
    record?.cancel();
  }

  // Storage failures and overload enter a bounded, coalesced recovery: one
  // timer per ref, paced by the shared scheduler, that schedules the ordinary
  // authoritative hydration. clear-suppressed refs never reach here (clear()
  // cancels repair and revoke is not the clear path), so suppression never
  // arms a repair loop. Without a scheduler (reducer-level tests) the same
  // recovery runs immediately.
  function armRepair(ref: string, entry: Entry): void {
    if (disposed || !ready || entry.owners.background <= 0) return;
    if (entry.repair) return; // coalesced: one outstanding repair per ref
    entry.repairAttempt += 1;
    const attempt = entry.repairAttempt;
    const run = () => {
      if (disposed || !ready || entry.owners.background <= 0) return;
      void hydrate(ref, held(entry) ? "foreground" : "background");
    };
    const record = {
      attempt,
      cancel: () => {},
    } as { cancel: () => void; attempt: number };
    const fire = () => {
      if (entry.repair !== record) return; // superseded or cancelled
      entry.repair = undefined;
      run();
    };
    entry.repair = record;
    const scheduler = port.retryScheduler?.();
    if (scheduler) {
      record.cancel = scheduler(attempt, fire);
    } else {
      entry.repair = undefined;
      run();
    }
  }

  function settleHydration(task: HydrationTask, error?: unknown): void {
    if (task.settled) return;
    task.settled = true;
    if (task.started) networkHydrations -= 1;
    hydrationTasks.delete(task);
    if (hydrationByRef.get(task.ref) === task) hydrationByRef.delete(task.ref);
    const queuedIndex = hydrationQueue.indexOf(task);
    if (queuedIndex >= 0) hydrationQueue.splice(queuedIndex, 1);
    if (!task.ownerSettled) {
      task.ownerSettled = true;
      if (error === undefined) task.resolve();
      else task.reject(error);
    }
    startAvailableHydrations();
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

  function retireHydration(task: HydrationTask): void {
    if (task.settled || task.retired) return;
    task.retired = true;
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

  async function persist(ref: string, entry: Entry): Promise<boolean> {
    if (entry.needsRecovery) {
      releaseEntryPending(entry);
      return false;
    }
    const writeGeneration = connectionGeneration;
    const ownerGeneration = entry.ownershipGeneration;
    const writeEpoch = clearEpoch;
    const resident = port.resident(ref);
    entry.residentPostCutBytes = 0;
    let model = resident;
    let loadedSequence = 0;
    let batchBytes = 0;
    const batch: ReceivedTranscriptUpdate[] = [];
    while (entry.pending[0] && (batch.length === 0 || batchBytes + entry.pending[0].bytes <= 256 * 1024)) {
      const update = entry.pending.shift();
      if (!update) break;
      batch.push(update);
      batchBytes += update.bytes;
    }
    entry.inFlight = batch;
    entry.workSequence = Math.max(entry.workSequence, batch.at(-1)?.sequence ?? entry.checkpointSequence);
    if (!model) {
      const found = await port.cache.getCurrent([ownerToken, ref], Date.now());
      if (!found?.record || found.epoch !== found.record.clearEpoch) {
        entry.persistence = "repairing";
        releaseEntryPending(entry);
        if (entry.inFlight === batch) entry.inFlight = [];
        if (batchBytes > 0) pendingBudget.release(batchBytes);
        void hydrate(ref, held(entry) ? "foreground" : "background");
        return false;
      }
      model = threadModelFromCurrentState(found.record.state, port.admission(ref));
      // A write can commit before an ownership change rejects its completion.
      // The retry still needs a fresh certificate, but must not replay updates
      // already represented by the checkpoint it actually loaded.
      if (found.record.connectionGeneration === writeGeneration && found.record.clearEpoch === writeEpoch) {
        loadedSequence = found.record.appliedSequence;
      }
    }
    const bytes = encodedPayloadBytes(currentTranscriptState(model));
    if (!Number.isFinite(bytes) || bytes > 32 * 1024 * 1024) {
      entry.persistence = "repairing";
      releaseEntryPending(entry);
      if (entry.inFlight === batch) entry.inFlight = [];
      if (batchBytes > 0) pendingBudget.release(batchBytes);
      void hydrate(ref, held(entry) ? "foreground" : "background");
      return false;
    }
    workerBytes = bytes;
    let committed = false;
    try {
      for (const update of batch) {
        if (update.sequence > loadedSequence) model = port.fold(ref, model, update);
      }
      const state = currentTranscriptState(model);
      if (!state) {
        entry.persistence = "repairing";
        return false;
      }
      const record: CurrentSessionRecord = {
        ownerToken,
        ref,
        connectionGeneration,
        ownershipGeneration: entry.ownershipGeneration,
        clearEpoch,
        appliedSequence: Math.max(loadedSequence, batch.at(-1)?.sequence ?? entry.appliedSequence),
        savedAt: Date.now(),
        state,
      };
      const recordBytes = encodedPayloadBytes(record);
      if (!Number.isFinite(recordBytes) || recordBytes > 32 * 1024 * 1024) {
        entry.persistence = "repairing";
        return false;
      }
      workerBytes = recordBytes;
      if (
        !ready ||
        entry.needsRecovery ||
        writeGeneration !== connectionGeneration ||
        ownerGeneration !== entry.ownershipGeneration ||
        writeEpoch !== clearEpoch ||
        !owned(entry) ||
        entry.persistence === "clear-suppressed"
      ) {
        entry.current = false;
        // Suppression is intentional: it never writes. When it prevents the
        // checkpoint and only background demand remains, release the excess
        // resident payload instead of holding an unwritable model. The entry's
        // metadata stays (unverified), so an explicit open recovers through the
        // ordinary cache-seeded authoritative path.
        if (
          entry.persistence === "clear-suppressed" &&
          !held(entry) &&
          entry.owners.background > 0 &&
          port.resident(ref)
        ) {
          await port.unload(ref);
          if (!port.resident(ref)) {
            if (entry.residentBytes > 0) residentBudget.release(entry.residentBytes);
            if (entry.handoffBytes > 0) pendingBudget.release(entry.handoffBytes);
            entry.residentBytes = 0;
            entry.handoffBytes = 0;
          }
        }
        return false;
      }
      workerCheckpointObserverForTests?.({ ref, resident, model, record, batch });
      const outcome = await port.cache.putCurrent(record, Date.now());
      if (outcome.outcome !== "written") {
        residency.revoke(ref, "storage");
        return false;
      }
      committedWrites += 1;
      if (
        !ready ||
        entry.needsRecovery ||
        writeGeneration !== connectionGeneration ||
        ownerGeneration !== entry.ownershipGeneration ||
        writeEpoch !== clearEpoch ||
        !owned(entry)
      ) {
        entry.current = false;
        return false;
      }
      if (entry.residentBytes > 0 && port.resident(ref)) {
        const exactWithLaterDeltas = bytes + entry.residentPostCutBytes;
        residentBudget.release(entry.residentBytes);
        if (residentBudget.reserve(exactWithLaterDeltas)) entry.residentBytes = exactWithLaterDeltas;
        else residentBudget.reserve(entry.residentBytes);
      }
      entry.residentPostCutBytes = 0;
      entry.checkpointSequence = record.appliedSequence;
      entry.current = !entry.needsRecovery && record.appliedSequence >= entry.appliedSequence;
      entry.checkpointOwnershipGeneration = ownerGeneration;
      if (!entry.needsRecovery) entry.persistence = "enabled";
      if (
        entry.current &&
        (entry.evictAfterCommit || (entry.residentBytes === 0 && entry.handoffBytes > 0)) &&
        !held(entry) &&
        entry.owners.background > 0
      ) {
        await port.unload(ref);
        if (!port.resident(ref) && entry.residentBytes > 0) {
          residentBudget.release(entry.residentBytes);
          entry.residentBytes = 0;
        }
        if (!port.resident(ref) && entry.handoffBytes > 0) {
          pendingBudget.release(entry.handoffBytes);
          entry.handoffBytes = 0;
        }
        if (!port.resident(ref)) entry.evictAfterCommit = false;
      }
      committed = true;
      cancelRepair(entry);
      return true;
    } finally {
      workerBytes = 0;
      if (entry.inFlight === batch) entry.inFlight = [];
      const retryForOwnerChange =
        !committed &&
        ready &&
        writeGeneration === connectionGeneration &&
        writeEpoch === clearEpoch &&
        owned(entry) &&
        ownerGeneration !== entry.ownershipGeneration;
      if (retryForOwnerChange) entry.pending.unshift(...batch);
      else if (batchBytes > 0) pendingBudget.release(batchBytes);
    }
  }

  async function runWorker(): Promise<void> {
    if (drainPromise || disposed) return drainPromise;
    drainPromise = (async () => {
      while (!disposed && (urgentQueue.length > 0 || workQueue.length > 0)) {
        const ref = urgentQueue.shift() ?? workQueue.shift();
        if (!ref) continue;
        const entry = entries.get(ref);
        if (!entry) continue;
        if (entry.handoffBytes > 0 && port.resident(ref) && residentBudget.reserve(entry.handoffBytes)) {
          pendingBudget.release(entry.handoffBytes);
          entry.residentBytes = entry.handoffBytes;
          entry.handoffBytes = 0;
        }
        diskWorkers = 1;
        const work = persist(ref, entry);
        entry.work = work;
        try {
          await work;
        } catch {
          residency.revoke(ref, "storage");
        } finally {
          if (entry.work === work) entry.work = undefined;
          entry.workSequence = entry.checkpointSequence;
          diskWorkers = 0;
          resolveWaiters(entry);
        }
        if (entry.pending.length > 0 && entry.persistence === "enabled") enqueue(ref);
        else if (entry.appliedSequence > entry.checkpointSequence && entry.persistence === "enabled")
          scheduleResidentWrite(ref, entry);
      }
    })().finally(() => {
      drainPromise = undefined;
      if (!disposed && (urgentQueue.length > 0 || workQueue.length > 0)) void runWorker();
    });
    return drainPromise;
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

  function hydrate(ref: string, intent: "background" | "foreground"): Promise<void> {
    return residency.scheduleHydration(ref, intent);
  }

  // After a successful disk promotion, recheck the durable epoch the lookup
  // transaction captured. A delayed or lost sibling-clear broadcast cannot
  // leave this read/publication race undetected: on a newer epoch, suppress
  // persistence (the publication stays as stale display state) and reconcile
  // owned refs through the ordinary paced hydration path. A failed epoch
  // lookup is a storage fault, not proof of a clear.
  function schedulePromotionEpochRecheck(
    ref: string,
    entry: Entry,
    promotionEpoch: number,
    guardGeneration: number,
    guardClearEpoch: number,
    guardOwnershipGeneration: number,
  ): void {
    const task = (async () => {
      const observed = await port.cache.readEpoch();
      if (disposed) return;
      // The same client/owner/claim generation guards completion, so an older
      // promotion cannot invalidate a newer lifetime.
      if (
        guardGeneration !== connectionGeneration ||
        guardClearEpoch !== clearEpoch ||
        entry.ownershipGeneration !== guardOwnershipGeneration ||
        !owned(entry)
      )
        return;
      if (observed === promotionEpoch) return;
      if (observed === undefined) {
        residency.revoke(ref, "storage");
        return;
      }
      residency.clear(observed);
      for (const [ownedRef, ownedEntry] of entries) {
        if (!owned(ownedEntry) || !ready || disposed) continue;
        void hydrate(ownedRef, held(ownedEntry) ? "foreground" : "background");
      }
    })().catch(() => {});
    epochRechecks.add(task);
    void task.then(() => {
      epochRechecks.delete(task);
    });
  }

  const residency: TranscriptResidency = {
    owners(ref, owners) {
      const entry = entryFor(ref);
      const previouslyOwned = owned(entry);
      const previouslyHeld = held(entry);
      const ownershipChanged =
        entry.owners.foreground !== owners.foreground ||
        entry.owners.history !== owners.history ||
        entry.owners.mutationPinned !== owners.mutationPinned ||
        entry.owners.background !== owners.background;
      entry.owners = { ...owners };
      if (ownershipChanged) {
        entry.ownershipGeneration += 1;
        if (!owned(entry)) entry.current = false;
        else if (!previouslyOwned) entry.current = false;
      }
      if (entry.persistence === "clear-suppressed") {
        const nowHeld = held(entry);
        if (previouslyHeld && !nowHeld) {
          // The last pre-clear foreground/history/pin holder released: a later
          // acquisition may now capture a new lifetime.
          entry.clearSuppressionHoldsPreClearHolders = false;
        } else if (!previouslyHeld && nowHeld && !entry.clearSuppressionHoldsPreClearHolders) {
          // An explicit foreground acquisition begun only after the pre-clear
          // holders finished. Its authoritative hydration may restore writes.
          entry.freshForegroundLifetime = true;
        }
      }
      if (previouslyOwned && !owned(entry)) {
        const task = hydrationByRef.get(ref);
        if (task) retireHydration(task);
      }
      if (!owned(entry) || entry.owners.background <= 0) cancelRepair(entry);
      if (held(entry)) {
        if (entry.residentBytes > 0) residentBudget.release(entry.residentBytes);
        if (entry.handoffBytes > 0) pendingBudget.release(entry.handoffBytes);
        entry.residentBytes = 0;
        entry.handoffBytes = 0;
      }
      if (owners.foreground > 0) entry.lastForegroundUse = Date.now();
      if (previouslyHeld && !held(entry) && owners.background > 0) {
        const model = port.resident(ref);
        if (model) residency.published(ref, model, entry.appliedSequence);
      } else if (!held(entry) && owners.background > 0 && port.resident(ref)) enqueue(ref);
    },
    removed(ref) {
      const task = hydrationByRef.get(ref);
      if (task) retireHydration(task);
      const entry = entries.get(ref);
      if (!entry) return;
      cancelRepair(entry);
      if (entry.residentBytes > 0) residentBudget.release(entry.residentBytes);
      if (entry.handoffBytes > 0) pendingBudget.release(entry.handoffBytes);
      entry.residentBytes = 0;
      entry.handoffBytes = 0;
      entry.current = false;
    },
    connection(generation, connected) {
      if (generation !== connectionGeneration || connected !== ready) {
        connectionGeneration = generation;
        ready = connected;
        if (!connected) {
          retireHydrations();
          for (const entry of entries.values()) {
            entry.current = false;
            cancelRepair(entry);
          }
        }
      }
    },
    accept(ref, update) {
      const entry = entryFor(ref);
      if (entry.owners.background <= 0) return false;
      if (entry.needsRecovery) return true;
      if (!pendingBudget.reserve(update.bytes)) {
        residency.revoke(ref, "gap");
        return true;
      }
      entry.pending.push(update);
      if (!port.resident(ref) && !entry.promoting) enqueue(ref);
      return !port.resident(ref);
    },
    published(ref, model, appliedSequence, authoritative = false) {
      const entry = entryFor(ref);
      if (authoritative) {
        cancelRepair(entry);
        entry.needsRecovery = false;
        if (entry.persistence !== "clear-suppressed") {
          entry.persistence = "enabled";
        } else if (entry.freshForegroundLifetime && held(entry)) {
          // A qualifying explicit foreground acquisition hydrated
          // authoritatively: capture the current epoch and resume writes.
          entry.persistence = "enabled";
          entry.freshForegroundLifetime = false;
        }
        // Otherwise clear suppression stands: background release/reacquire and
        // reconnect alone never re-enable caching, and a pane that predates the
        // clear keeps its suppressed lifetime until its holders release.
      }
      const previouslyApplied = entry.appliedSequence;
      const consumed = entry.pending.filter((update) => update.sequence <= appliedSequence);
      const incrementalBytes = consumed.reduce((sum, update) => sum + update.bytes, 0);
      if (entry.work && appliedSequence > entry.checkpointSequence) entry.residentPostCutBytes += incrementalBytes;
      const handoffOwnsModel = entry.residentBytes === 0 && entry.handoffBytes > 0;
      entry.appliedSequence = Math.max(entry.appliedSequence, appliedSequence);
      if (entry.appliedSequence > entry.checkpointSequence) entry.current = false;
      if (consumed.length > 0) {
        if (!handoffOwnsModel) pendingBudget.release(incrementalBytes);
        entry.pending = entry.pending.filter((update) => update.sequence > appliedSequence);
      }
      if (held(entry) || entry.owners.background <= 0) return true;
      const size =
        (entry.residentBytes > 0 || handoffOwnsModel) && appliedSequence > previouslyApplied
          ? entry.residentBytes + entry.handoffBytes + incrementalBytes
          : encodedPayloadBytes(currentTranscriptState(model));
      if (!Number.isFinite(size)) {
        residency.revoke(ref, "storage");
        return false;
      }
      const old = entry.residentBytes;
      if (old > 0) residentBudget.release(old);
      if (!residentBudget.reserve(size)) {
        entry.residentBytes = 0;
        if (!held(entry) && entry.owners.background > 0) {
          if (handoffOwnsModel) entry.handoffBytes = size;
          else if (pendingBudget.reserve(size)) entry.handoffBytes = size;
          if (entry.handoffBytes > 0) {
            enqueueLeastRecentlyUsedVictims(ref, size);
            queueMicrotask(() => enqueue(ref));
          } else {
            if (old > 0 && residentBudget.reserve(old)) entry.residentBytes = old;
            residency.revoke(ref, "storage");
            return false;
          }
        } else return false;
        return true;
      }
      entry.residentBytes = size;
      if (!entry.needsRecovery && !held(entry) && entry.owners.background > 0) scheduleResidentWrite(ref, entry);
      return true;
    },
    reservePending(_ref, bytes) {
      return pendingBudget.reserve(bytes);
    },
    releasePending(_ref, bytes) {
      pendingBudget.release(bytes);
    },
    async promote(ref) {
      const entry = entryFor(ref);
      const resident = port.resident(ref);
      if (resident) return resident;
      if (!entry.current && entry.pending.length === 0 && !entry.work) return undefined;
      entry.promoting = true;
      try {
        const promotionCut = Math.max(entry.pending.at(-1)?.sequence ?? 0, entry.workSequence, entry.appliedSequence);
        if (!(await waitForEntryCut(ref, entry, promotionCut))) return undefined;
        const activeGeneration = connectionGeneration;
        const ownershipGeneration = entry.ownershipGeneration;
        const activeClearEpoch = clearEpoch;
        const found = await port.cache.getCurrent([ownerToken, ref], Date.now());
        const record = found?.record;
        if (
          record &&
          found.epoch === activeClearEpoch &&
          record.clearEpoch === activeClearEpoch &&
          record.connectionGeneration === activeGeneration &&
          record.ownershipGeneration === entry.checkpointOwnershipGeneration &&
          ready &&
          activeGeneration === connectionGeneration &&
          activeClearEpoch === clearEpoch &&
          entry.current &&
          !entry.needsRecovery &&
          entry.persistence === "enabled" &&
          owned(entry)
        ) {
          let model = threadModelFromCurrentState(record.state, port.admission(ref));
          const uncheckpointed = [...entry.inFlight, ...entry.pending]
            .filter((update) => update.sequence > record.appliedSequence)
            .sort((left, right) => left.sequence - right.sequence);
          for (const update of uncheckpointed) model = port.fold(ref, model, update);
          if (
            !ready ||
            activeGeneration !== connectionGeneration ||
            activeClearEpoch !== clearEpoch ||
            entry.ownershipGeneration !== ownershipGeneration ||
            !entry.current ||
            entry.needsRecovery ||
            entry.persistence !== "enabled" ||
            !owned(entry)
          )
            return undefined;
          port.publish(ref, model);
          entry.appliedSequence = uncheckpointed.at(-1)?.sequence ?? record.appliedSequence;
          releaseEntryPending(entry);
          entry.current = true;
          schedulePromotionEpochRecheck(
            ref,
            entry,
            found.epoch,
            activeGeneration,
            activeClearEpoch,
            ownershipGeneration,
          );
          return model;
        }
        return undefined;
      } finally {
        entry.promoting = false;
        if (entry.pending.length > 0 && entry.persistence === "enabled") enqueue(ref, held(entry));
      }
    },
    scheduleHydration(ref, intent, run) {
      if (!ready || disposed) return Promise.resolve();
      const existing = hydrationByRef.get(ref);
      if (existing) {
        if (existing.queued && intent === "foreground") {
          existing.intent = "foreground";
          hydrationQueue.sort((a, b) => Number(b.intent === "foreground") - Number(a.intent === "foreground"));
        }
        return existing.promise;
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
        run: run ?? ((signal) => port.hydrate(ref, intent, signal)),
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
      hydrationByRef.set(ref, task);
      if (networkHydrations < options.networkHydrations) startHydration(task);
      else {
        task.queued = true;
        hydrationQueue.push(task);
        hydrationQueue.sort((a, b) => Number(b.intent === "foreground") - Number(a.intent === "foreground"));
      }
      return promise;
    },
    async drain(ref) {
      const entry = entries.get(ref);
      if (!entry) return;
      enqueue(ref, held(entry));
      await new Promise<void>((resolve) => entry.waiters.push(resolve));
    },
    revoke(ref, _reason) {
      const entry = entryFor(ref);
      releaseEntryPending(entry);
      entry.current = false;
      entry.needsRecovery = _reason !== "deleted";
      entry.persistence = "repairing";
      if (_reason === "deleted") cancelRepair(entry);
      else armRepair(ref, entry);
    },
    clear(epoch) {
      clearEpoch = epoch;
      for (const entry of entries.values()) {
        cancelRepair(entry);
        entry.current = false;
        entry.persistence = "clear-suppressed";
        entry.clearSuppressionHoldsPreClearHolders = held(entry);
        entry.freshForegroundLifetime = false;
        if (entry.flushTimer) clearTimeout(entry.flushTimer);
        if (entry.maxWaitTimer) clearTimeout(entry.maxWaitTimer);
        entry.flushTimer = undefined;
        entry.maxWaitTimer = undefined;
        releaseEntryPending(entry);
      }
      retireHydrations();
    },
    dispose() {
      disposed = true;
      retireHydrations();
      epochRechecks.clear();
      for (const entry of entries.values()) {
        cancelRepair(entry);
        if (entry.flushTimer) clearTimeout(entry.flushTimer);
        if (entry.maxWaitTimer) clearTimeout(entry.maxWaitTimer);
        releaseEntryPending(entry);
      }
      entries.clear();
      workQueue.length = 0;
      urgentQueue.length = 0;
    },
    async settled() {
      while (true) {
        for (const [ref, entry] of entries) {
          if (entry.flushTimer) enqueue(ref);
        }
        await runWorker();
        if (epochRechecks.size > 0) {
          await Promise.allSettled([...epochRechecks]);
          continue;
        }
        if (
          !drainPromise &&
          workQueue.length === 0 &&
          urgentQueue.length === 0 &&
          [...entries.values()].every((entry) => !entry.work && !entry.flushTimer)
        )
          return;
      }
    },
    snapshot() {
      return {
        residentBytes: residentBudget.used,
        pendingBytes: pendingBudget.used,
        workerBytes,
        diskWorkers,
        networkHydrations,
        queuedHydrationRefs: hydrationQueue.map((task) => task.ref),
        committedWrites,
        refs: new Map(
          [...entries].map(([ref, entry]) => [
            ref,
            {
              resident: port.resident(ref) !== undefined,
              residentBytes: entry.residentBytes,
              handoffBytes: entry.handoffBytes,
              current: entry.current,
              appliedSequence: entry.appliedSequence,
              persistence: entry.persistence,
              pending: entry.pending,
              inFlight: entry.inFlight,
            },
          ]),
        ),
      };
    },
  };
  return residency;
}
