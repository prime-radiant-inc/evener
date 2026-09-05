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
  onWriteStalled?: (waiting: boolean) => void;
  // Storage-fault seam used to prove IndexedDB rollback at commit boundaries.
  beforeCommit?: (operation: MutationOutboxOperation) => void;
}

const DATABASE_NAME = "evener-mutation-outbox";
const DATABASE_VERSION = 2;
const OUTBOX_STORE = "outbox";
const OPTIMISTIC_STORE = "optimistic";
const RECOVERY_STORE = "recovery";
const SEQUENCE_STORE = "sequences";
const TARGET_SEQUENCE_INDEX = "byTargetSequence";
const STORAGE_WAIT_MS = 10_000;

export class MutationStorageTimeoutError extends Error {
  constructor() {
    super("Browser message storage is not responding. Your draft has been kept. Try again when storage recovers.");
    this.name = "MutationStorageTimeoutError";
  }
}

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
  readonly #onWriteStalled: ((waiting: boolean) => void) | undefined;
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
            if (this.#stalledWrites === 1) this.#onWriteStalled?.(true);
            return;
          }
        }
        reject(new MutationStorageTimeoutError());
      }, STORAGE_WAIT_MS);
    });
    try {
      const work = body(transaction).then((result) => {
        if (operation) this.#beforeCommit?.(operation);
        return result;
      });
      // Observe request and transaction failures together. An abort must
      // release the caller even when a request callback never arrives.
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
        if (this.#stalledWrites === 0) this.#onWriteStalled?.(false);
      }
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
