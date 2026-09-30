import type { CachedSessionRecord } from "@evener/appwire-client";
import { trackProjectionWork } from "./projectionWork";

const DATABASE_NAME = "evener-session-cache";
// Version 1 is a compatibility fence, not a schema migration: a future record
// shape change bumps this so an old tab fails its opens closed (a cache miss)
// instead of sharing rows with a shape it cannot decode.
const DATABASE_VERSION = 1;
const RECORDS_STORE = "records";
const META_STORE = "meta";
// The meta row holding the durable clear epoch. Not a record's meta row: it
// carries no savedAt, is exempt from expiry and eviction by construction, and
// both enumerations skip it by key comparison.
const EPOCH_ROW_KEY = "__clearEpoch";
export const SESSION_CACHE_MAX_BYTES = 32 * 1024 * 1024;
export const SESSION_CACHE_TTL_DAYS = 14;
const TTL_MS = SESSION_CACHE_TTL_DAYS * 24 * 60 * 60 * 1000;
// The lookup's own short deadline (the spec's load seam), far below the
// outbox's 10-second storage timeout: no pane ever waits on storage longer.
export const SESSION_CACHE_LOOKUP_DEADLINE_MS = 250;
const STORAGE_WAIT_MS = 10_000;

export type SessionCacheOpenDiagnosticPath =
  | "open-timeout"
  | "open-blocked"
  | "upgrade-abandoned"
  | "versionchange-retire"
  | "version-fence";
export interface SessionCacheOpenDiagnostic {
  database: string;
  version: number;
  path: SessionCacheOpenDiagnosticPath;
  versionchangeTransaction: boolean;
}
export interface SessionCacheIndexedDBOptions {
  indexedDB?: IDBFactory;
  databaseName?: string;
  onOpenDiagnostic?: (diagnostic: SessionCacheOpenDiagnostic) => void;
}
export type SessionCacheWriteOutcome =
  | { outcome: "written" }
  | { outcome: "aborted"; observedEpoch: number }
  | { outcome: "oversize" }
  | { outcome: "failed" };

interface CacheMetaRow {
  ref: string;
  bytes: number;
  savedAt: number;
}
interface EpochRow {
  ref: typeof EPOCH_ROW_KEY;
  epoch: number;
}

export function warnSessionCacheOpenDiagnostic(diagnostic: SessionCacheOpenDiagnostic): void {
  console.warn("evener session cache:", diagnostic);
}
export const DEFAULT_OPEN_DIAGNOSTIC: (d: SessionCacheOpenDiagnostic) => void =
  import.meta.env.MODE === "test" ? () => {} : warnSessionCacheOpenDiagnostic;

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
      () => {
        /* the abort listener settles failure */
      },
      { once: true },
    );
  });
}

// The JSON boundary: a record is plain data. A row that fails to decode is a
// miss, and the caller deletes it; storage never hands the store a value it
// did not encode. `history` presence is the shape check.
function decodeRecord(row: unknown): CachedSessionRecord | undefined {
  const candidate = row as CachedSessionRecord | undefined;
  if (candidate === undefined || typeof candidate !== "object") return undefined;
  if (candidate.history === undefined || candidate.ref === undefined) return undefined;
  return candidate;
}

export class SessionCacheIndexedDB {
  readonly #indexedDB: IDBFactory;
  readonly #databaseName: string;
  #database: IDBDatabase | undefined;
  #databasePromise: Promise<IDBDatabase> | undefined;
  readonly #onOpenDiagnostic: (d: SessionCacheOpenDiagnostic) => void;

  constructor(options: SessionCacheIndexedDBOptions = {}) {
    this.#indexedDB = options.indexedDB ?? globalThis.indexedDB;
    this.#databaseName = options.databaseName ?? DATABASE_NAME;
    this.#onOpenDiagnostic = options.onOpenDiagnostic ?? DEFAULT_OPEN_DIAGNOSTIC;
  }

  isOpen(): boolean {
    return this.#database !== undefined;
  }
  close(): void {
    this.#database?.close();
    this.#database = undefined;
    this.#databasePromise = undefined;
  }

  async get(ref: string, now: number): Promise<{ record: CachedSessionRecord; epoch: number } | undefined> {
    // A readwrite transaction so an expired (or corrupt) row can be deleted
    // in the same step that found it: the guarantee is that an expired record
    // does not survive any storage access that sees it.
    return this.#readwrite("get", async (tx) => {
      const epochRow = await requestResult(
        tx.objectStore(META_STORE).get(EPOCH_ROW_KEY) as IDBRequest<EpochRow | undefined>,
      );
      const row = await requestResult(tx.objectStore(RECORDS_STORE).get(ref));
      const record = decodeRecord(row);
      if (record === undefined) {
        if (row !== undefined) await this.#deleteRows(tx, ref); // corrupt: a miss, never a throw, never a leftover
        return undefined;
      }
      const meta = await requestResult(tx.objectStore(META_STORE).get(ref) as IDBRequest<CacheMetaRow | undefined>);
      if (meta !== undefined && meta.savedAt + TTL_MS <= now) {
        await this.#deleteRows(tx, ref);
        return undefined;
      }
      return { record, epoch: epochRow?.epoch ?? 0 };
    });
  }

  // put/clear/deleteRecords/count arrive in Tasks 3 and 4; declared now so
  // the class compiles with stubs that throw "not implemented in this task".
  async put(_record: CachedSessionRecord, _scheduledEpoch: number, _now: number): Promise<SessionCacheWriteOutcome> {
    throw new Error("put: implemented in Task 3");
  }
  async clear(): Promise<{ committed: boolean; epoch: number }> {
    throw new Error("clear: implemented in Task 4");
  }
  async deleteRecords(_refs: string[]): Promise<boolean> {
    throw new Error("deleteRecords: implemented in Task 4");
  }
  async count(): Promise<number | undefined> {
    throw new Error("count: implemented in Task 4");
  }

  async #deleteRows(tx: IDBTransaction, ref: string): Promise<void> {
    void tx.objectStore(RECORDS_STORE).delete(ref);
    void tx.objectStore(META_STORE).delete(ref);
  }

  #transaction<T>(stores: string[], mode: IDBTransactionMode, body: (tx: IDBTransaction) => Promise<T>): Promise<T> {
    return trackProjectionWork(this.#runTransaction(stores, mode, body));
  }

  async #readwrite<T>(_label: string, body: (tx: IDBTransaction) => Promise<T>): Promise<T | undefined> {
    try {
      return await this.#transaction([RECORDS_STORE, META_STORE], "readwrite", body);
    } catch {
      return undefined; // every failure is a miss; the diagnostic seam carries the why
    }
  }

  // One attempt per call: a timeout fails this call and the next call tries
  // the open afresh (the outbox's rule). The lookup's Promise.race against
  // SESSION_CACHE_LOOKUP_DEADLINE_MS lives in the store seam, not here.
  async #runTransaction<T>(
    stores: string[],
    mode: IDBTransactionMode,
    body: (tx: IDBTransaction) => Promise<T>,
  ): Promise<T> {
    const database = await this.#open();
    const tx = database.transaction(stores, mode);
    const work = body(tx);
    const completion = transactionCompletion(tx);
    await work; // requests issued; auto-commit happens when the microtask queue drains
    await completion;
    return work;
  }

  #reportOpenDiagnostic(path: SessionCacheOpenDiagnosticPath, versionchangeTransaction: boolean): void {
    try {
      this.#onOpenDiagnostic({
        database: this.#databaseName,
        version: DATABASE_VERSION,
        path,
        versionchangeTransaction,
      });
    } catch {
      // A throwing reporter cannot change the storage outcome.
    }
  }

  #open(): Promise<IDBDatabase> {
    if (this.#database) return Promise.resolve(this.#database);
    if (this.#databasePromise) return this.#databasePromise;
    const opening = new Promise<IDBDatabase>((resolve, reject) => {
      const request = this.#indexedDB.open(this.#databaseName, DATABASE_VERSION);
      let abandoned = false;
      const timer = setTimeout(() => {
        abandoned = true;
        this.#reportOpenDiagnostic("open-timeout", Boolean(request.transaction));
        reject(new Error("session cache open timed out"));
      }, STORAGE_WAIT_MS);
      request.addEventListener(
        "upgradeneeded",
        () => {
          const database = request.result;
          if (!database.objectStoreNames.contains(RECORDS_STORE)) {
            database.createObjectStore(RECORDS_STORE, { keyPath: "ref" });
          }
          if (!database.objectStoreNames.contains(META_STORE)) {
            database.createObjectStore(META_STORE, { keyPath: "ref" });
          }
          // Seed the epoch row so every reader sees a number, never an absent row.
          const tx = request.transaction;
          if (tx !== null) tx.objectStore(META_STORE).put({ ref: EPOCH_ROW_KEY, epoch: 0 } satisfies EpochRow);
        },
        { once: true },
      );
      request.addEventListener(
        "success",
        () => {
          if (abandoned) return;
          clearTimeout(timer);
          this.#database = request.result;
          this.#databasePromise = undefined;
          resolve(request.result);
        },
        { once: true },
      );
      request.addEventListener(
        "error",
        () => {
          if (abandoned) return;
          clearTimeout(timer);
          this.#reportOpenDiagnostic("version-fence", request.error?.name === "VersionError");
          reject(request.error ?? new Error("session cache open failed"));
        },
        { once: true },
      );
      request.addEventListener(
        "blocked",
        () => {
          this.#reportOpenDiagnostic("open-blocked", Boolean(request.transaction));
        },
        { once: true },
      );
    });
    this.#databasePromise = opening;
    opening.catch(() => {
      this.#databasePromise = undefined;
    });
    return opening;
  }
}
