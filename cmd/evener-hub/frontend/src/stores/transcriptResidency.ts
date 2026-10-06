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
}

export interface TranscriptResidency {
  owners(ref: string, owners: TranscriptOwners): void;
  connection(generation: number, ready: boolean): void;
  accept(ref: string, update: ReceivedTranscriptUpdate): boolean;
  published(ref: string, model: ThreadModel, appliedSequence: number): void;
  reservePending(ref: string, bytes: number): boolean;
  releasePending(ref: string, bytes: number): void;
  promote(ref: string): Promise<ThreadModel | undefined>;
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
  committedWrites: number;
  refs: ReadonlyMap<
    string,
    {
      resident: boolean;
      current: boolean;
      appliedSequence: number;
      persistence: "enabled" | "repairing" | "clear-suppressed";
    }
  >;
}

type Persistence = "enabled" | "repairing" | "clear-suppressed";
interface Entry {
  owners: TranscriptOwners;
  ownershipGeneration: number;
  checkpointOwnershipGeneration: number;
  appliedSequence: number;
  checkpointSequence: number;
  residentBytes: number;
  handoffBytes: number;
  pending: ReceivedTranscriptUpdate[];
  current: boolean;
  evictAfterCommit: boolean;
  persistence: Persistence;
  lastForegroundUse: number;
  promoting: boolean;
  flushTimer?: ReturnType<typeof setTimeout>;
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
  const hydrationQueue: Array<{ ref: string; intent: "background" | "foreground"; run: () => Promise<void> }> = [];
  const controllers = new Map<string, AbortController>();
  let connectionGeneration = 0;
  let ready = false;
  let clearEpoch = 0;
  let workerBytes = 0;
  let diskWorkers = 0;
  let networkHydrations = 0;
  let committedWrites = 0;
  let disposed = false;
  let drainPromise: Promise<void> | undefined;

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
      handoffBytes: 0,
      pending: [],
      current: false,
      evictAfterCommit: false,
      persistence: "enabled",
      lastForegroundUse: 0,
      promoting: false,
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
    if (entry.flushTimer || entry.work) return;
    entry.flushTimer = setTimeout(() => {
      entry.flushTimer = undefined;
      enqueue(ref);
    }, 100);
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

  async function persist(ref: string, entry: Entry): Promise<boolean> {
    const writeGeneration = connectionGeneration;
    const ownerGeneration = entry.ownershipGeneration;
    const writeEpoch = clearEpoch;
    const resident = port.resident(ref);
    let model = resident;
    let batchBytes = 0;
    const batch: ReceivedTranscriptUpdate[] = [];
    while (entry.pending[0] && (batch.length === 0 || batchBytes + entry.pending[0].bytes <= 256 * 1024)) {
      const update = entry.pending.shift();
      if (!update) break;
      batch.push(update);
      batchBytes += update.bytes;
    }
    if (!model) {
      const found = await port.cache.getCurrent([ownerToken, ref], Date.now());
      if (!found?.record || found.epoch !== found.record.clearEpoch) {
        entry.persistence = "repairing";
        releaseEntryPending(entry);
        if (batchBytes > 0) pendingBudget.release(batchBytes);
        void hydrate(ref, held(entry) ? "foreground" : "background");
        return false;
      }
      model = threadModelFromCurrentState(found.record.state, port.admission(ref));
    }
    const bytes = encodedPayloadBytes(currentTranscriptState(model));
    if (!Number.isFinite(bytes) || bytes > 32 * 1024 * 1024) {
      entry.persistence = "repairing";
      releaseEntryPending(entry);
      if (batchBytes > 0) pendingBudget.release(batchBytes);
      void hydrate(ref, held(entry) ? "foreground" : "background");
      return false;
    }
    workerBytes = bytes;
    let committed = false;
    try {
      for (const update of batch) model = port.fold(ref, model, update);
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
        appliedSequence: batch.at(-1)?.sequence ?? entry.appliedSequence,
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
        writeGeneration !== connectionGeneration ||
        ownerGeneration !== entry.ownershipGeneration ||
        writeEpoch !== clearEpoch ||
        !owned(entry) ||
        entry.persistence === "clear-suppressed"
      ) {
        entry.current = false;
        return false;
      }
      const outcome = await port.cache.putCurrent(record, Date.now());
      if (outcome.outcome !== "written") {
        entry.persistence = "repairing";
        return false;
      }
      committedWrites += 1;
      if (
        !ready ||
        writeGeneration !== connectionGeneration ||
        ownerGeneration !== entry.ownershipGeneration ||
        writeEpoch !== clearEpoch ||
        !owned(entry)
      ) {
        entry.current = false;
        return false;
      }
      entry.checkpointSequence = record.appliedSequence;
      entry.current = record.appliedSequence >= entry.appliedSequence;
      entry.checkpointOwnershipGeneration = ownerGeneration;
      entry.persistence = "enabled";
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
      return true;
    } finally {
      workerBytes = 0;
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
          entry.persistence = "repairing";
        } finally {
          if (entry.work === work) entry.work = undefined;
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

  async function hydrate(ref: string, intent: "background" | "foreground"): Promise<void> {
    if (!ready || disposed) return;
    const controller = new AbortController();
    controllers.set(ref, controller);
    const run = async (): Promise<void> => {
      networkHydrations += 1;
      try {
        await port.hydrate(ref, intent, controller.signal);
      } finally {
        networkHydrations -= 1;
        controllers.delete(ref);
      }
    };
    if (networkHydrations < options.networkHydrations) await run();
    else {
      await new Promise<void>((resolve, reject) => {
        hydrationQueue.push({
          ref,
          intent,
          run: async () => {
            try {
              await run();
              resolve();
            } catch (error) {
              reject(error);
            }
          },
        });
        hydrationQueue.sort((a, b) => Number(b.intent === "foreground") - Number(a.intent === "foreground"));
      });
    }
    while (networkHydrations < options.networkHydrations && hydrationQueue.length > 0) {
      const queued = hydrationQueue.shift();
      if (queued) void queued.run();
    }
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
      if (owners.foreground > 0) entry.lastForegroundUse = Date.now();
      if (previouslyHeld && !held(entry) && owners.background > 0) {
        const model = port.resident(ref);
        if (model) residency.published(ref, model, entry.appliedSequence);
      } else if (!held(entry) && owners.background > 0 && port.resident(ref)) enqueue(ref);
    },
    connection(generation, connected) {
      if (generation !== connectionGeneration || connected !== ready) {
        connectionGeneration = generation;
        ready = connected;
        if (!connected) {
          for (const controller of controllers.values()) controller.abort();
          for (const entry of entries.values()) entry.current = false;
        }
      }
    },
    accept(ref, update) {
      const entry = entryFor(ref);
      if (entry.owners.background <= 0) return false;
      if (!pendingBudget.reserve(update.bytes)) {
        residency.revoke(ref, "gap");
        return true;
      }
      entry.pending.push(update);
      if (!port.resident(ref) && !entry.promoting) enqueue(ref);
      return !port.resident(ref);
    },
    published(ref, model, appliedSequence) {
      const entry = entryFor(ref);
      const previouslyApplied = entry.appliedSequence;
      const consumed = entry.pending.filter((update) => update.sequence <= appliedSequence);
      const incrementalBytes = consumed.reduce((sum, update) => sum + update.bytes, 0);
      const handoffOwnsModel = entry.residentBytes === 0 && entry.handoffBytes > 0;
      entry.appliedSequence = Math.max(entry.appliedSequence, appliedSequence);
      if (entry.appliedSequence > entry.checkpointSequence) entry.current = false;
      if (consumed.length > 0) {
        if (!handoffOwnsModel) pendingBudget.release(incrementalBytes);
        entry.pending = entry.pending.filter((update) => update.sequence > appliedSequence);
      }
      if (held(entry) || entry.owners.background <= 0) return;
      const size =
        (entry.residentBytes > 0 || handoffOwnsModel) && appliedSequence > previouslyApplied
          ? entry.residentBytes + entry.handoffBytes + incrementalBytes
          : encodedPayloadBytes(currentTranscriptState(model));
      if (!Number.isFinite(size)) {
        residency.revoke(ref, "storage");
        return;
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
            enqueue(ref);
          } else {
            entry.current = false;
            entry.persistence = "repairing";
            port.unload(ref);
          }
        }
        return;
      }
      entry.residentBytes = size;
      if (!held(entry) && entry.owners.background > 0) scheduleResidentWrite(ref, entry);
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
      while (entry.pending.length > 0 || entry.work) {
        if (!entry.work) enqueue(ref, true);
        await runWorker();
      }
      const activeGeneration = connectionGeneration;
      const ownershipGeneration = entry.ownershipGeneration;
      try {
        const found = await port.cache.getCurrent([ownerToken, ref], Date.now());
        if (
          found?.record &&
          found.epoch === clearEpoch &&
          found.record.clearEpoch === clearEpoch &&
          found.record.connectionGeneration === activeGeneration &&
          found.record.ownershipGeneration === entry.checkpointOwnershipGeneration &&
          ready &&
          activeGeneration === connectionGeneration
        ) {
          let model = threadModelFromCurrentState(found.record.state, port.admission(ref));
          for (const update of entry.pending) model = port.fold(ref, model, update);
          if (!ready || activeGeneration !== connectionGeneration || entry.ownershipGeneration !== ownershipGeneration)
            return undefined;
          port.publish(ref, model);
          entry.appliedSequence = entry.pending.at(-1)?.sequence ?? found.record.appliedSequence;
          releaseEntryPending(entry);
          entry.current = true;
          return model;
        }
        return undefined;
      } finally {
        entry.promoting = false;
      }
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
      entry.persistence = "repairing";
      if (_reason !== "deleted" && entry.owners.background > 0 && ready) {
        void hydrate(ref, held(entry) ? "foreground" : "background");
      }
    },
    clear(epoch) {
      clearEpoch = epoch;
      for (const entry of entries.values()) {
        entry.current = false;
        entry.persistence = "clear-suppressed";
        if (entry.flushTimer) clearTimeout(entry.flushTimer);
        entry.flushTimer = undefined;
        releaseEntryPending(entry);
      }
      for (const controller of controllers.values()) controller.abort();
    },
    dispose() {
      disposed = true;
      for (const controller of controllers.values()) controller.abort();
      for (const entry of entries.values()) {
        if (entry.flushTimer) clearTimeout(entry.flushTimer);
        releaseEntryPending(entry);
      }
      entries.clear();
      workQueue.length = 0;
      urgentQueue.length = 0;
      hydrationQueue.length = 0;
    },
    async settled() {
      while (true) {
        for (const [ref, entry] of entries) {
          if (entry.flushTimer) enqueue(ref);
        }
        await runWorker();
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
        committedWrites,
        refs: new Map(
          [...entries].map(([ref, entry]) => [
            ref,
            {
              resident: port.resident(ref) !== undefined,
              current: entry.current,
              appliedSequence: entry.appliedSequence,
              persistence: entry.persistence,
            },
          ]),
        ),
      };
    },
  };
  return residency;
}
