import type { InputItem } from "../protocol/types.gen";
import type {
  MutationAttachment,
  MutationIntent,
  MutationOptimisticRecord,
  MutationOutboxRecord,
  MutationOutboxState,
  MutationRecoveryKind,
  MutationRecoveryRecord,
} from "./mutationOutbox";
import { createSecureUUID } from "./secureUUID";

type MutationOutboxOperation =
  | "enqueueIntent"
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
  // Storage-fault seam used to prove IndexedDB rollback at commit boundaries.
  beforeCommit?: (operation: MutationOutboxOperation) => void;
}

const DATABASE_NAME = "serf-mutation-outbox";
const DATABASE_VERSION = 2;
const OUTBOX_STORE = "outbox";
const OPTIMISTIC_STORE = "optimistic";
const RECOVERY_STORE = "recovery";
const SEQUENCE_STORE = "sequences";
const TARGET_SEQUENCE_INDEX = "byTargetSequence";

interface TargetSequence {
  targetRef: string;
  lastSequence: number;
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
  }

  close(): void {
    this.#database?.close();
    this.#database = undefined;
    this.#databasePromise = undefined;
  }

  async enqueueIntent(intent: MutationIntent): Promise<MutationOutboxRecord> {
    if (!intent.targetRef.trim()) throw new Error("targetRef is required");
    return this.#write([OUTBOX_STORE, SEQUENCE_STORE], "enqueueIntent", async (transaction) => {
      const intentSequence = await this.#allocateSequence(transaction, intent.targetRef);
      const clientMutationId = this.#createMutationId();
      const record: MutationOutboxRecord = {
        ...intent,
        payload: { ...intent.payload, clientMutationId },
        version: 1,
        clientMutationId,
        intentSequence,
        createdAt: this.#now(),
        state: "submitting",
      };
      await requestResult(transaction.objectStore(OUTBOX_STORE).add(record));
      return record;
    });
  }

  async getOutbox(clientMutationId: string): Promise<MutationOutboxRecord | undefined> {
    return this.#read(OUTBOX_STORE, async (transaction) => {
      return requestResult<MutationOutboxRecord | undefined>(
        transaction.objectStore(OUTBOX_STORE).get(clientMutationId),
      );
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
    return this.#write([OUTBOX_STORE, OPTIMISTIC_STORE, RECOVERY_STORE], "settleReceipt", async (transaction) => {
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

      const display = source.optimisticDisplay;
      const retainsOptimisticDisplay =
        projectionState === "pending" &&
        display !== null &&
        typeof display === "object" &&
        "input" in display &&
        Array.isArray(display.input);
      if (retainsOptimisticDisplay) {
        const accepted: MutationOptimisticRecord = {
          version: source.version,
          clientMutationId: source.clientMutationId,
          intentSequence: source.intentSequence,
          createdAt: source.createdAt,
          targetRef: source.targetRef,
          threadId: source.threadId,
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
    });
  }

  async settleApplied(clientMutationId: string): Promise<boolean> {
    return this.#write([OUTBOX_STORE, OPTIMISTIC_STORE, RECOVERY_STORE], undefined, async (transaction) => {
      const outbox = transaction.objectStore(OUTBOX_STORE);
      const optimistic = transaction.objectStore(OPTIMISTIC_STORE);
      const recovery = transaction.objectStore(RECOVERY_STORE);
      const [outboxRecord, optimisticRecord, recoveryRecord] = await Promise.all([
        requestResult<MutationOutboxRecord | undefined>(outbox.get(clientMutationId)),
        requestResult<MutationOptimisticRecord | undefined>(optimistic.get(clientMutationId)),
        requestResult<MutationRecoveryRecord | undefined>(recovery.get(clientMutationId)),
      ]);
      if (!outboxRecord && !optimisticRecord && !recoveryRecord) return false;
      if (outboxRecord) await requestResult(outbox.delete(clientMutationId));
      if (optimisticRecord) await requestResult(optimistic.delete(clientMutationId));
      if (recoveryRecord) await requestResult(recovery.delete(clientMutationId));
      return true;
    });
  }

  async markUnknown(clientMutationId: string, state: MutationOutboxState): Promise<boolean> {
    return this.#write(OUTBOX_STORE, undefined, async (transaction) => {
      const store = transaction.objectStore(OUTBOX_STORE);
      const record = await requestResult<MutationOutboxRecord | undefined>(store.get(clientMutationId));
      if (!record) return false;
      if (record.state !== state) await requestResult(store.put({ ...record, state }));
      return true;
    });
  }

  // restoreProvenAbsent is markUnknown's exit. A blockedUnknown record waits
  // for its outcome to become provable ("retry must remain blocked until
  // persistence recovers" — the daemon's NormalizeClientMutationError); a
  // successful authoritative read is that proof. An id absent from every
  // authoritative set was never journaled, so it returns to "submitting" for
  // the normal dispatch path — the daemon's journal replays a receipt if a
  // race ever makes the resend a duplicate. Ids the authority DOES report are
  // left alone: reconcileIdentities or a replayed dispatch owns their
  // settlement. Returns the restored ids.
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
  ): Promise<MutationRecoveryRecord | undefined> {
    return this.#write([OUTBOX_STORE, RECOVERY_STORE], "transferToRecovery", async (transaction) => {
      const outbox = transaction.objectStore(OUTBOX_STORE);
      const record = await requestResult<MutationOutboxRecord | undefined>(outbox.get(clientMutationId));
      if (!record) return undefined;
      const recovery: MutationRecoveryRecord = { ...record, recoveryKind };
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
      };
      await requestResult(store.put(next));
      return next;
    });
  }

  async discardRecovery(clientMutationId: string): Promise<boolean> {
    return this.#write(RECOVERY_STORE, "discardRecovery", async (transaction) => {
      const store = transaction.objectStore(RECOVERY_STORE);
      const record = await requestResult<MutationRecoveryRecord | undefined>(store.get(clientMutationId));
      if (!record) return false;
      await requestResult(store.delete(clientMutationId));
      return true;
    });
  }

  async resendRecovery(clientMutationId: string, intent: MutationIntent): Promise<MutationOutboxRecord | undefined> {
    if (!intent.targetRef.trim()) throw new Error("targetRef is required");
    return this.#write([OUTBOX_STORE, RECOVERY_STORE, SEQUENCE_STORE], "resendRecovery", async (transaction) => {
      const recoveryStore = transaction.objectStore(RECOVERY_STORE);
      const recovery = await requestResult<MutationRecoveryRecord | undefined>(recoveryStore.get(clientMutationId));
      if (!recovery) return undefined;
      const intentSequence = await this.#allocateSequence(transaction, intent.targetRef);
      const nextMutationId = this.#createMutationId();
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
        intentSequence,
        createdAt: this.#now(),
        state: "submitting",
      };
      await requestResult(transaction.objectStore(OUTBOX_STORE).add(record));
      await requestResult(recoveryStore.delete(clientMutationId));
      return record;
    });
  }

  async nextDispatchable(targetRef: string): Promise<MutationOutboxRecord | undefined> {
    const records = await this.listOutbox(targetRef);
    const first = records[0];
    return first?.state === "submitting" ? first : undefined;
  }

  async #open(): Promise<IDBDatabase> {
    if (this.#database) return this.#database;
    this.#databasePromise ??= new Promise((resolve, reject) => {
      const request = this.#indexedDB.open(this.#databaseName, DATABASE_VERSION);
      request.addEventListener(
        "upgradeneeded",
        () => {
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
          this.#database = request.result;
          this.#database.addEventListener("versionchange", () => this.close());
          resolve(this.#database);
        },
        { once: true },
      );
      request.addEventListener("error", () => reject(request.error ?? new Error("Unable to open mutation outbox")), {
        once: true,
      });
      request.addEventListener("blocked", () => reject(new Error("Mutation outbox upgrade is blocked")), {
        once: true,
      });
    });
    return this.#databasePromise;
  }

  async #read<T>(stores: string | string[], body: (transaction: IDBTransaction) => Promise<T>): Promise<T> {
    const database = await this.#open();
    const transaction = database.transaction(stores, "readonly");
    const completed = transactionCompletion(transaction);
    const result = await body(transaction);
    await completed;
    return result;
  }

  async #write<T>(
    stores: string | string[],
    operation: MutationOutboxOperation | undefined,
    body: (transaction: IDBTransaction) => Promise<T>,
  ): Promise<T> {
    const database = await this.#open();
    const transaction = database.transaction(stores, "readwrite");
    const completed = transactionCompletion(transaction);
    try {
      const result = await body(transaction);
      if (operation) this.#beforeCommit?.(operation);
      await completed;
      return result;
    } catch (error) {
      try {
        transaction.abort();
      } catch {
        // The transaction already completed; preserve the original failure.
      }
      await completed.catch(() => undefined);
      throw error;
    }
  }

  async #allocateSequence(transaction: IDBTransaction, targetRef: string): Promise<number> {
    const store = transaction.objectStore(SEQUENCE_STORE);
    const current = await requestResult<TargetSequence | undefined>(store.get(targetRef));
    const next = (current?.lastSequence ?? 0) + 1;
    await requestResult(store.put({ targetRef, lastSequence: next } satisfies TargetSequence));
    return next;
  }
}
