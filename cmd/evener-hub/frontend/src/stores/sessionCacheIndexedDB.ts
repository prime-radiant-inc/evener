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
  // The cap on the sum of encoded record bytes. The spec pins the default;
  // injection is the test discipline, the way the debounce intervals are
  // injected, so tests exercise eviction with small records.
  maxBytes?: number;
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
  readonly #maxBytes: number;
  #database: IDBDatabase | undefined;
  #databasePromise: Promise<IDBDatabase> | undefined;
  readonly #onOpenDiagnostic: (d: SessionCacheOpenDiagnostic) => void;

  constructor(options: SessionCacheIndexedDBOptions = {}) {
    this.#indexedDB = options.indexedDB ?? globalThis.indexedDB;
    this.#databaseName = options.databaseName ?? DATABASE_NAME;
    this.#maxBytes = options.maxBytes ?? SESSION_CACHE_MAX_BYTES;
    this.#onOpenDiagnostic = options.onOpenDiagnostic ?? DEFAULT_OPEN_DIAGNOSTIC;
  }

  isOpen(): boolean {
    return this.#database !== undefined;
  }
  close(): void {
    // Drop the connection we hold and forget it, so a later call simply opens
    // again. An open still in flight keeps running: its success lands after
    // this, finds its promise superseded, and closes the late connection.
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

  async put(record: CachedSessionRecord, scheduledEpoch: number, now: number): Promise<SessionCacheWriteOutcome> {
    // bytes by construction: the meta row stores the encoded length; the body
    // carries no bytes field of its own (a length stored inside the body it
    // measures cannot be exact).
    const encoded = JSON.stringify(record);
    const bytes = encoded.length;
    let observed = 0;
    const written = await this.#readwriteOutcome("put", async (tx) => {
      const metaStore = tx.objectStore(META_STORE);
      const epochRow = await requestResult(metaStore.get(EPOCH_ROW_KEY) as IDBRequest<EpochRow | undefined>);
      observed = epochRow?.epoch ?? 0;
      if (observed > scheduledEpoch) return { outcome: "aborted", observedEpoch: observed } as SessionCacheWriteOutcome;

      const rows = await requestResult(metaStore.getAll() as IDBRequest<CacheMetaRow[]>);
      const live: CacheMetaRow[] = [];
      for (const row of rows) {
        if (row.ref === EPOCH_ROW_KEY) continue; // exempt by construction: no savedAt, never evicted or expired
        if (row.savedAt + TTL_MS <= now) {
          await this.#deleteRows(tx, row.ref); // expiry runs inside every write transaction
          continue;
        }
        live.push(row);
      }
      if (bytes > this.#maxBytes) {
        await this.#deleteRows(tx, record.ref); // a session that outgrew its cache leaves nothing stale behind
        return { outcome: "oversize" } as SessionCacheWriteOutcome;
      }
      const previous = live.find((row) => row.ref === record.ref);
      if (previous) live.splice(live.indexOf(previous), 1); // a re-write replaces its own accounting
      metaStore.put({ ref: record.ref, bytes, savedAt: record.savedAt } satisfies CacheMetaRow);
      tx.objectStore(RECORDS_STORE).put(JSON.parse(encoded) as CachedSessionRecord);
      let total = live.reduce((sum, row) => sum + row.bytes, 0) + bytes;
      live.sort((a, b) => a.savedAt - b.savedAt); // whole-record LRU by last write
      for (const victim of live) {
        if (total <= this.#maxBytes) break;
        await this.#deleteRows(tx, victim.ref);
        total -= victim.bytes;
      }
      return { outcome: "written" } as SessionCacheWriteOutcome;
    });
    return written ?? ({ outcome: "aborted", observedEpoch: observed } as SessionCacheWriteOutcome);
  }

  // clear/deleteRecords/count arrive in Task 4; declared now so the class
  // compiles with stubs that throw "not implemented in this task".
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

  // The write failure discipline: #readwrite turns a failure into a miss;
  // a write turns the same failure into the "failed" outcome instead - open,
  // transaction, and quota errors all drop silently here, never a throw.
  async #readwriteOutcome(
    label: string,
    body: (tx: IDBTransaction) => Promise<SessionCacheWriteOutcome>,
  ): Promise<SessionCacheWriteOutcome> {
    return (await this.#readwrite(label, body)) ?? { outcome: "failed" };
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

  // One attempt per call: a timeout fails this call and the next call tries
  // the open afresh (the outbox's rule). The lookup's Promise.race against
  // SESSION_CACHE_LOOKUP_DEADLINE_MS lives in the store seam, not here.
  async #open(): Promise<IDBDatabase> {
    // A stuck open is not remembered: every later call attempts the open
    // again, so a read after storage recovers still succeeds.
    if (this.#database) return Promise.resolve(this.#database);
    if (this.#databasePromise) return this.#databasePromise;
    const opening = new Promise<IDBDatabase>((resolve, reject) => {
      const request = this.#indexedDB.open(this.#databaseName, DATABASE_VERSION);
      let abandoned = false;
      const timer = setTimeout(() => {
        // The watchdog fired: fail this one attempt. A later call attempts
        // the open again.
        abandoned = true;
        this.#reportOpenDiagnostic("open-timeout", Boolean(request.transaction));
        reject(new Error("session cache open timed out"));
      }, STORAGE_WAIT_MS);
      request.addEventListener(
        "upgradeneeded",
        () => {
          // The schema upgrade must always be allowed to commit, even for an
          // open this adapter has already abandoned or superseded: aborting
          // a versionchange/upgrade transaction is the documented trigger for
          // Chromium's wedged connection coordinator (crbug 40278488), after
          // which open() never fires success, error, or blocked. `abandoned`
          // and the success handler's identity guard below decide only
          // whether the late success installs its connection, never whether
          // the upgrade commits; recording the abandoned attempt is the
          // whole reaction, and a later call opens afresh, which is what
          // makes recovery after a stalled upgrade possible.
          if (abandoned || this.#databasePromise !== opening) {
            this.#reportOpenDiagnostic("upgrade-abandoned", Boolean(request.transaction));
          }
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
          clearTimeout(timer);
          const database = request.result;
          // A close() (or a newer attempt) superseded this open, or the
          // watchdog abandoned it: never install the late connection. Close
          // it so nothing outlives the adapter's lifecycle, and fail this
          // call, which reads as a miss.
          if (abandoned || this.#databasePromise !== opening) {
            database.close();
            reject(new Error("session cache open was superseded"));
            return;
          }
          this.#database = database;
          this.#databasePromise = undefined;
          database.addEventListener("versionchange", () => {
            // Another connection is taking the database to a new version, so
            // this one must close. No versionchange transaction is in
            // progress on it; the upgrade belongs to the other connection's
            // request.
            this.#reportOpenDiagnostic("versionchange-retire", false);
            this.#retire(database);
          });
          database.addEventListener("close", () => this.#retire(database));
          resolve(database);
        },
        { once: true },
      );
      request.addEventListener(
        "error",
        () => {
          if (abandoned) return;
          clearTimeout(timer);
          const error = request.error ?? new Error("session cache open failed");
          // The fence is the one open error a diagnostic path names: this
          // build's version is below the stored database's, a newer tab owns
          // the schema, and this tab fails closed (a miss). No versionchange
          // transaction of ours was live - the upgrade belongs to the newer
          // connection - and any other open error stays a silent failed
          // open; inventing a generic path is out of bounds.
          if (error.name === "VersionError") this.#reportOpenDiagnostic("version-fence", false);
          reject(error);
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
    try {
      return await opening;
    } catch (error) {
      // Forget the attempt only if it is still the current one: a close()
      // during this open already cleared the field, and a newer attempt's
      // promise must survive this attempt's failure.
      if (this.#databasePromise === opening) this.#databasePromise = undefined;
      throw error;
    }
  }

  #retire(database: IDBDatabase): void {
    // A versionchange or a browser-side close ended this connection: drop it
    // so the next call opens afresh instead of reusing a dead handle, and
    // only when it is still the installed one.
    database.close();
    if (this.#database !== database) return;
    this.#database = undefined;
    this.#databasePromise = undefined;
  }
}
