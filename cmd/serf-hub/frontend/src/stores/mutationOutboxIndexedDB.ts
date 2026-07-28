import type {
  MutationIntent,
  MutationOutboxRecord,
  MutationOutboxState,
  MutationRecoveryKind,
  MutationRecoveryRecord,
  RecoveryResendTarget,
} from "./mutationOutbox";

type MutationOutboxOperation = "enqueueIntent" | "transferToRecovery" | "resendRecovery";

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
const DATABASE_VERSION = 1;
const OUTBOX_STORE = "outbox";
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
    this.#createMutationId = options.createMutationId ?? (() => crypto.randomUUID());
    this.#createPresentationId = options.createPresentationId ?? (() => crypto.randomUUID());
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
    const records = await this.listOutbox();
    return [...new Set(records.map((record) => record.targetRef))].sort();
  }

  async settleApplied(clientMutationId: string): Promise<boolean> {
    return this.#write([OUTBOX_STORE, RECOVERY_STORE], undefined, async (transaction) => {
      const outbox = transaction.objectStore(OUTBOX_STORE);
      const recovery = transaction.objectStore(RECOVERY_STORE);
      const [outboxRecord, recoveryRecord] = await Promise.all([
        requestResult<MutationOutboxRecord | undefined>(outbox.get(clientMutationId)),
        requestResult<MutationRecoveryRecord | undefined>(recovery.get(clientMutationId)),
      ]);
      if (!outboxRecord && !recoveryRecord) return false;
      if (outboxRecord) await requestResult(outbox.delete(clientMutationId));
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

  async resendRecovery(
    clientMutationId: string,
    target: RecoveryResendTarget,
  ): Promise<MutationOutboxRecord | undefined> {
    if (!target.targetRef.trim()) throw new Error("targetRef is required");
    return this.#write([OUTBOX_STORE, RECOVERY_STORE, SEQUENCE_STORE], "resendRecovery", async (transaction) => {
      const recoveryStore = transaction.objectStore(RECOVERY_STORE);
      const recovery = await requestResult<MutationRecoveryRecord | undefined>(recoveryStore.get(clientMutationId));
      if (!recovery) return undefined;
      const intentSequence = await this.#allocateSequence(transaction, target.targetRef);
      const nextMutationId = this.#createMutationId();
      const attachments = recovery.attachments.map((attachment) => ({
        ...attachment,
        presentationId: this.#createPresentationId(),
      }));
      const record: MutationOutboxRecord = {
        ...recovery,
        targetRef: target.targetRef,
        threadId: target.threadId,
        payload: {
          ...recovery.payload,
          ref: target.targetRef,
          threadId: target.threadId,
          clientMutationId: nextMutationId,
        },
        attachments,
        clientMutationId: nextMutationId,
        intentSequence,
        createdAt: this.#now(),
        state: "submitting",
      };
      delete (record as Partial<MutationRecoveryRecord>).recoveryKind;
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
