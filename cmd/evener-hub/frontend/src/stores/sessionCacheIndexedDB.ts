import {
  type CachedSessionRecord,
  type CurrentTranscriptState,
  decodeCurrentTranscriptState,
} from "@evener/appwire-client";
import { IDBConnection, requestResult, transactionCompletion, tryAbortTransaction } from "./idbConnection";
import { trackProjectionWork } from "./projectionWork";
import { upgradeSessionCache } from "./sessionCacheSchema";
import { countUtf8Bytes } from "./transcriptPayloadBudget";

const DATABASE_NAME = "evener-session-cache";
const DATABASE_VERSION = 2;
const RECORDS_STORE = "records";
const META_STORE = "meta";
const CURRENT_STORE = "current";
const CURRENT_META_STORE = "currentMeta";
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
  databaseVersion?: number;
  // The cap on the sum of encoded record bytes. The spec pins the default;
  // injection is the test discipline, the way the debounce intervals are
  // injected, so tests exercise eviction with small records.
  maxBytes?: number;
  // Storage-fault seam used to prove IndexedDB rollback at commit boundaries
  // (the outbox's beforeCommit): invoked as the last step inside each write
  // transaction's body. Tests may use the transaction to inject request-level
  // failures; these are simulated storage faults, not browser quota errors.
  beforeCommit?: (operation: "put" | "putCurrent" | "clear" | "deleteRecords", tx: IDBTransaction) => void;
  onOpenDiagnostic?: (diagnostic: SessionCacheOpenDiagnostic) => void;
}
export type SessionCacheWriteOutcome =
  | { outcome: "written"; evictedCurrent?: readonly CurrentSessionKey[] }
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

export type CurrentSessionKey = readonly [ownerToken: string, ref: string];
export interface CurrentSessionRecord {
  ownerToken: string;
  ref: string;
  connectionGeneration: number;
  ownershipGeneration: number;
  clearEpoch: number;
  appliedSequence: number;
  savedAt: number;
  state: CurrentTranscriptState;
}
export interface CurrentSessionLookup {
  record: CurrentSessionRecord | undefined;
  epoch: number;
}
interface CurrentMetaRow {
  ownerToken: string;
  ref: string;
  bytes: number;
  savedAt: number;
}

const CACHE_STORES = [RECORDS_STORE, META_STORE, CURRENT_STORE, CURRENT_META_STORE];

export function warnSessionCacheOpenDiagnostic(diagnostic: SessionCacheOpenDiagnostic): void {
  console.warn("evener session cache:", diagnostic);
}
export const DEFAULT_OPEN_DIAGNOSTIC: (d: SessionCacheOpenDiagnostic) => void =
  import.meta.env.MODE === "test" ? () => {} : warnSessionCacheOpenDiagnostic;

// The JSON boundary: a record is plain data. A row that fails to decode is a
// miss, and the caller deletes it; storage never hands the store a value it
// did not encode. Check the required nested shape before the shell constructor
// or position scanners can see it. Optional positions must also be usable.
function decodeRecord(row: unknown): CachedSessionRecord | undefined {
  if (!isObject(row)) return undefined;
  if (![row.ref, row.threadId, row.name, row.modelProvider, row.model].every((value) => typeof value === "string")) {
    return undefined;
  }
  if (!Number.isFinite(row.savedAt)) return undefined;
  if (![row.imageSessionId, row.olderCursor].every((value) => value === undefined || typeof value === "string")) {
    return undefined;
  }
  const history = row.history;
  if (!isObject(history) || typeof history.bootGeneration !== "string") return undefined;
  if (history.incarnation !== undefined && typeof history.incarnation !== "string") return undefined;
  if (![history.epoch, history.length, history.appliedGeneration, history.issuedGeneration].every(Number.isFinite)) {
    return undefined;
  }
  if (!Array.isArray(history.turns)) return undefined;
  for (const turn of history.turns) {
    if (
      !isObject(turn) ||
      typeof turn.id !== "string" ||
      typeof turn.status !== "string" ||
      !Array.isArray(turn.items)
    ) {
      return undefined;
    }
    for (const item of turn.items) {
      if (
        !isObject(item) ||
        ![item.id, item.turnId, item.type, item.text].every((value) => typeof value === "string") ||
        (item.textOmitted !== undefined && item.textOmitted !== true)
      ) {
        return undefined;
      }
      if (
        item.position !== undefined &&
        (!isObject(item.position) || !Number.isFinite(item.position.entry) || !Number.isFinite(item.position.item))
      ) {
        return undefined;
      }
    }
  }
  return row as unknown as CachedSessionRecord;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export class SessionCacheIndexedDB {
  readonly #connection: IDBConnection;
  readonly #databaseName: string;
  readonly #databaseVersion: number;
  readonly #maxBytes: number;
  #observedEpoch: number | undefined;
  readonly #writeListeners = new Set<() => void>();
  readonly #onOpenDiagnostic: (d: SessionCacheOpenDiagnostic) => void;
  readonly #beforeCommit:
    | ((operation: "put" | "putCurrent" | "clear" | "deleteRecords", tx: IDBTransaction) => void)
    | undefined;

  constructor(options: SessionCacheIndexedDBOptions = {}) {
    this.#databaseName = options.databaseName ?? DATABASE_NAME;
    this.#databaseVersion = options.databaseVersion ?? DATABASE_VERSION;
    this.#maxBytes = options.maxBytes ?? SESSION_CACHE_MAX_BYTES;
    this.#onOpenDiagnostic = options.onOpenDiagnostic ?? DEFAULT_OPEN_DIAGNOSTIC;
    this.#beforeCommit = options.beforeCommit;
    this.#connection = new IDBConnection({
      indexedDB: options.indexedDB ?? globalThis.indexedDB,
      databaseName: this.#databaseName,
      databaseVersion: this.#databaseVersion,
      waitMs: STORAGE_WAIT_MS,
      upgrade: (database) => this.#upgrade(database),
      prepare: (database) =>
        this.#onDatabaseTransaction(database, CACHE_STORES, "readwrite", (tx) => this.#sweepExpired(tx, Date.now())),
      errors: {
        open: "session cache open failed",
        superseded: "session cache open was superseded",
        timeout: () => new Error("session cache open timed out"),
      },
      reportDiagnostic: (path, active) => this.#reportOpenDiagnostic(path, active),
      reportOpenError: (error) => {
        // Only VersionError names a diagnostic path: this tab's schema is
        // older than the stored database. Every other open error is a miss.
        if (error.name === "VersionError") this.#reportOpenDiagnostic("version-fence", false);
      },
    });
  }

  isOpen(): boolean {
    return this.#connection.isOpen();
  }

  get observedEpoch(): number | undefined {
    return this.#observedEpoch;
  }

  subscribeWrites(listener: () => void): () => void {
    this.#writeListeners.add(listener);
    return () => {
      this.#writeListeners.delete(listener);
    };
  }

  close(): void {
    this.#connection.close();
  }

  async get(ref: string, now: number): Promise<{ record: CachedSessionRecord; epoch: number } | undefined> {
    // A readwrite transaction so an expired (or corrupt) row can be deleted
    // in the same step that found it: the guarantee is that an expired record
    // does not survive any storage access that sees it.
    const found = await this.#readwrite(async (tx) => {
      // The epoch row, the record body, and its meta row are independent
      // keyed reads: issue all three in this one transaction and await them
      // together, then decide the deletes exactly as before - the same
      // decisions, without serial round trips.
      const [epochRow, row, meta] = await Promise.all([
        requestResult(tx.objectStore(META_STORE).get(EPOCH_ROW_KEY) as IDBRequest<EpochRow | undefined>),
        requestResult(tx.objectStore(RECORDS_STORE).get(ref)),
        requestResult(tx.objectStore(META_STORE).get(ref) as IDBRequest<CacheMetaRow | undefined>),
      ]);
      const epoch = epochRow?.epoch ?? 0;
      const record = decodeRecord(row);
      if (record === undefined) {
        if (row !== undefined) await this.#deleteRows(tx, ref); // corrupt: a miss, never a throw, never a leftover
        return { record: undefined, epoch };
      }
      if (meta !== undefined && (!isCacheMetaRow(meta) || meta.ref !== ref || meta.savedAt + TTL_MS <= now)) {
        await this.#deleteRows(tx, ref);
        return { record: undefined, epoch };
      }
      return { record, epoch };
    }, now);
    if (found === undefined) return undefined;
    this.#observedEpoch = Math.max(this.#observedEpoch ?? 0, found.epoch);
    return found.record === undefined ? undefined : { record: found.record, epoch: found.epoch };
  }

  async getCurrent(key: CurrentSessionKey, now: number): Promise<CurrentSessionLookup | undefined> {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const found = await Promise.race([
      this.#readwrite(async (tx) => {
        const [epochRow, row, meta] = await Promise.all([
          requestResult(tx.objectStore(META_STORE).get(EPOCH_ROW_KEY) as IDBRequest<EpochRow | undefined>),
          requestResult(tx.objectStore(CURRENT_STORE).get([...key])),
          requestResult(tx.objectStore(CURRENT_META_STORE).get([...key]) as IDBRequest<CurrentMetaRow | undefined>),
        ]);
        const epoch = epochRow?.epoch ?? 0;
        const record = decodeCurrentRecord(row);
        const validMeta = isCurrentMetaRow(meta) && meta.ownerToken === key[0] && meta.ref === key[1];
        if (
          !record ||
          record.ownerToken !== key[0] ||
          record.ref !== key[1] ||
          !validMeta ||
          meta.savedAt + TTL_MS <= now
        ) {
          if (row !== undefined || meta !== undefined) await this.#deleteCurrentRows(tx, key);
          return { record: undefined, epoch };
        }
        return { record, epoch };
      }, now),
      new Promise<undefined>((resolve) => {
        timer = setTimeout(() => resolve(undefined), SESSION_CACHE_LOOKUP_DEADLINE_MS);
      }),
    ]).finally(() => clearTimeout(timer));
    if (found === undefined) return undefined;
    this.#observedEpoch = Math.max(this.#observedEpoch ?? 0, found.epoch);
    return found;
  }

  async readEpoch(): Promise<number | undefined> {
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      return await Promise.race([
        this.#transaction([META_STORE], "readonly", async (tx) => {
          const row = await requestResult(
            tx.objectStore(META_STORE).get(EPOCH_ROW_KEY) as IDBRequest<EpochRow | undefined>,
          );
          const epoch = row?.epoch ?? 0;
          this.#observedEpoch = Math.max(this.#observedEpoch ?? 0, epoch);
          return epoch;
        }),
        new Promise<undefined>((resolve) => {
          timer = setTimeout(() => resolve(undefined), SESSION_CACHE_LOOKUP_DEADLINE_MS);
        }),
      ]);
    } catch {
      return undefined;
    } finally {
      clearTimeout(timer);
    }
  }

  async put(record: CachedSessionRecord, scheduledEpoch: number, now: number): Promise<SessionCacheWriteOutcome> {
    // UTF-8 JSON payload bytes, not JS UTF-16 code units or IndexedDB overhead.
    const encoded = JSON.stringify(record);
    const bytes = countUtf8Bytes(encoded);
    const result = await this.#readwriteOutcome(
      "put",
      async (tx) => {
        const metaStore = tx.objectStore(META_STORE);
        const epochRow = await requestResult(metaStore.get(EPOCH_ROW_KEY) as IDBRequest<EpochRow | undefined>);
        const observed = epochRow?.epoch ?? 0;
        if (observed > scheduledEpoch)
          return { outcome: "aborted", observedEpoch: observed } as SessionCacheWriteOutcome;

        const [rows, currentRows] = await Promise.all([
          requestResult(metaStore.getAll() as IDBRequest<CacheMetaRow[]>),
          requestResult(tx.objectStore(CURRENT_META_STORE).getAll() as IDBRequest<CurrentMetaRow[]>),
        ]);
        // A re-write replaces its own accounting, so its previous meta row is
        // not live; the epoch row is not a record's accounting either. Keys
        // are unique, so the exclusion below is exactly the set of rows the
        // old find-and-splice removed, in one pass.
        const live = rows.filter((row) => row.ref !== EPOCH_ROW_KEY && row.ref !== record.ref);
        if (bytes > this.#maxBytes) {
          await this.#deleteRows(tx, record.ref); // a session that outgrew its cache leaves nothing stale behind
          return { outcome: "oversize" } as SessionCacheWriteOutcome;
        }
        metaStore.put({ ref: record.ref, bytes, savedAt: record.savedAt } satisfies CacheMetaRow);
        tx.objectStore(RECORDS_STORE).put(JSON.parse(encoded) as CachedSessionRecord);
        let total =
          live.reduce((sum, row) => sum + row.bytes, 0) + currentRows.reduce((sum, row) => sum + row.bytes, 0) + bytes;
        if (total > this.#maxBytes) {
          live.sort((a, b) => a.savedAt - b.savedAt); // whole-record LRU by last write
          for (const victim of live) {
            if (total <= this.#maxBytes) break;
            await this.#deleteRows(tx, victim.ref);
            total -= victim.bytes;
          }
        }
        if (total > this.#maxBytes) {
          await this.#deleteRows(tx, record.ref);
          return { outcome: "oversize" } as SessionCacheWriteOutcome;
        }
        return { outcome: "written" } as SessionCacheWriteOutcome;
      },
      now,
    );
    if (result.outcome === "written") {
      for (const listener of this.#writeListeners) listener();
    }
    return result;
  }

  async putCurrent(record: CurrentSessionRecord, now: number): Promise<SessionCacheWriteOutcome> {
    const key: CurrentSessionKey = [record.ownerToken, record.ref];
    const encoded = JSON.stringify(record);
    const bytes = countUtf8Bytes(encoded);
    const result = await this.#readwriteOutcome(
      "putCurrent",
      async (tx) => {
        const metaStore = tx.objectStore(META_STORE);
        const currentMetaStore = tx.objectStore(CURRENT_META_STORE);
        const epochRow = await requestResult(metaStore.get(EPOCH_ROW_KEY) as IDBRequest<EpochRow | undefined>);
        const observed = epochRow?.epoch ?? 0;
        if (observed > record.clearEpoch) return { outcome: "aborted", observedEpoch: observed };

        const [historyRows, currentRows] = await Promise.all([
          requestResult(metaStore.getAll() as IDBRequest<CacheMetaRow[]>),
          requestResult(currentMetaStore.getAll() as IDBRequest<CurrentMetaRow[]>),
        ]);
        const history = historyRows.filter((row) => row.ref !== EPOCH_ROW_KEY);
        const current = currentRows.filter((row) => row.ownerToken !== record.ownerToken || row.ref !== record.ref);
        if (bytes > this.#maxBytes) {
          await this.#deleteCurrentRows(tx, key);
          return { outcome: "oversize" };
        }
        let total =
          history.reduce((sum, row) => sum + row.bytes, 0) + current.reduce((sum, row) => sum + row.bytes, 0) + bytes;
        const evictedCurrent: CurrentSessionKey[] = [];
        history.sort((a, b) => a.savedAt - b.savedAt);
        for (const victim of history) {
          if (total <= this.#maxBytes) break;
          await this.#deleteRows(tx, victim.ref);
          total -= victim.bytes;
        }
        if (total > this.#maxBytes) {
          current.sort((a, b) => a.savedAt - b.savedAt);
          for (const victim of current) {
            if (total <= this.#maxBytes) break;
            const victimKey: CurrentSessionKey = [victim.ownerToken, victim.ref];
            await this.#deleteCurrentRows(tx, victimKey);
            evictedCurrent.push(victimKey);
            total -= victim.bytes;
          }
        }
        if (total > this.#maxBytes) {
          await this.#deleteCurrentRows(tx, key);
          return { outcome: "oversize" };
        }
        currentMetaStore.put({
          ownerToken: record.ownerToken,
          ref: record.ref,
          bytes,
          savedAt: record.savedAt,
        } satisfies CurrentMetaRow);
        tx.objectStore(CURRENT_STORE).put(JSON.parse(encoded) as CurrentSessionRecord);
        return { outcome: "written", ...(evictedCurrent.length > 0 ? { evictedCurrent } : {}) };
      },
      now,
    );
    if (result.outcome === "written") for (const listener of this.#writeListeners) listener();
    return result;
  }

  // The commit-observed clear: one readwrite transaction deletes every
  // records row - the records store cleared whole, since it holds nothing
  // but records rows - and every meta row except the reserved epoch row, so
  // an orphan meta row no record backs dies too, and increments the epoch
  // row in the same commit. `committed` is read off the
  // value the transaction runner delivers, which only arrives after the
  // completion event - an abort (the fault
  // seam's throw, a request error, a commit-time failure) reads as
  // { committed: false, epoch: observed-before-the-abort }, so no caller ever
  // broadcasts a clear that did not land.
  async clear(): Promise<{ committed: boolean; epoch: number }> {
    let observed = 0;
    const landed = await this.#readwrite(async (tx) => {
      const metaStore = tx.objectStore(META_STORE);
      const recordsStore = tx.objectStore(RECORDS_STORE);
      const epochRow = await requestResult(metaStore.get(EPOCH_ROW_KEY) as IDBRequest<EpochRow | undefined>);
      observed = epochRow?.epoch ?? 0;
      recordsStore.clear();
      tx.objectStore(CURRENT_STORE).clear();
      tx.objectStore(CURRENT_META_STORE).clear();
      const metaKeys = await requestResult(metaStore.getAllKeys() as IDBRequest<IDBValidKey[]>);
      for (const ref of metaKeys) {
        if (ref === EPOCH_ROW_KEY) continue; // exempt by construction: the reserved epoch row survives every clear
        metaStore.delete(ref);
      }
      metaStore.put({ ref: EPOCH_ROW_KEY, epoch: observed + 1 } satisfies EpochRow);
      this.#beforeCommit?.("clear", tx);
      return observed + 1;
    });
    return landed === undefined ? { committed: false, epoch: observed } : { committed: true, epoch: landed };
  }

  // One transaction removes the named refs' rows, under clear's
  // delivered-result rule: true only when the transaction's completion was
  // observed, so an abort reads false and the caller retries idempotently.
  async deleteRecords(refs: string[]): Promise<boolean> {
    const landed = await this.#readwrite(async (tx) => {
      for (const ref of refs) await this.#deleteRows(tx, ref);
      for (const ref of refs) {
        const keys = await requestResult(tx.objectStore(CURRENT_STORE).index("ref").getAllKeys(ref));
        for (const key of keys) await this.#deleteCurrentRows(tx, key as unknown as CurrentSessionKey);
      }
      this.#beforeCommit?.("deleteRecords", tx);
      return true;
    });
    return landed ?? false;
  }

  // The settings row's reader: unique refs across recorded history and current
  // checkpoints, a miss (undefined) on every failure - a stalled or failed
  // open, a failed transaction - never a throw.
  async count(): Promise<number | undefined> {
    try {
      return await this.#transaction([RECORDS_STORE, CURRENT_META_STORE], "readonly", async (tx) => {
        const [history, current] = await Promise.all([
          requestResult(tx.objectStore(RECORDS_STORE).getAllKeys() as IDBRequest<IDBValidKey[]>),
          requestResult(tx.objectStore(CURRENT_META_STORE).getAll() as IDBRequest<CurrentMetaRow[]>),
        ]);
        const refs = new Set(history.filter((key) => key !== EPOCH_ROW_KEY).map(String));
        for (const row of current) refs.add(row.ref);
        return refs.size;
      });
    } catch {
      return undefined;
    }
  }

  async #deleteRows(tx: IDBTransaction, ref: string): Promise<void> {
    void tx.objectStore(RECORDS_STORE).delete(ref);
    void tx.objectStore(META_STORE).delete(ref);
  }

  async #deleteCurrentRows(tx: IDBTransaction, key: CurrentSessionKey): Promise<void> {
    void tx.objectStore(CURRENT_STORE).delete([...key]);
    void tx.objectStore(CURRENT_META_STORE).delete([...key]);
  }

  async #sweepExpired(tx: IDBTransaction, now: number): Promise<void> {
    const rows = await requestResult(tx.objectStore(META_STORE).getAll() as IDBRequest<CacheMetaRow[]>);
    for (const row of rows) {
      if (row.ref !== EPOCH_ROW_KEY && (!isCacheMetaRow(row) || row.savedAt + TTL_MS <= now)) {
        if (typeof row.ref === "string") await this.#deleteRows(tx, row.ref);
      }
    }
    const currentRows = await requestResult(
      tx.objectStore(CURRENT_META_STORE).getAll() as IDBRequest<CurrentMetaRow[]>,
    );
    for (const row of currentRows) {
      if (!isCurrentMetaRow(row) || row.savedAt + TTL_MS <= now) {
        if (isObject(row) && typeof row.ownerToken === "string" && typeof row.ref === "string") {
          await this.#deleteCurrentRows(tx, [row.ownerToken, row.ref]);
        }
      }
    }
  }

  #transaction<T>(stores: string[], mode: IDBTransactionMode, body: (tx: IDBTransaction) => Promise<T>): Promise<T> {
    return trackProjectionWork(this.#runTransaction(stores, mode, body));
  }

  async #readwrite<T>(body: (tx: IDBTransaction) => Promise<T>, now = Date.now()): Promise<T | undefined> {
    try {
      return await this.#transaction(CACHE_STORES, "readwrite", async (tx) => {
        await this.#sweepExpired(tx, now);
        return body(tx);
      });
    } catch {
      return undefined; // every failure is a miss; the diagnostic seam carries the why
    }
  }

  // The write failure discipline: #readwrite turns a failure into a miss;
  // a write turns the same failure into the "failed" outcome instead - open,
  // transaction, and quota errors all drop silently here, never a throw.
  // The beforeCommit seam fires inside this wrapper, after every completed
  // put body's requests are queued - the written, oversize, and stale-epoch
  // paths alike. A synchronous throw or a request failure injected through the
  // transaction aborts the real transaction, which proves rollback.
  async #readwriteOutcome(
    operation: "put" | "putCurrent",
    body: (tx: IDBTransaction) => Promise<SessionCacheWriteOutcome>,
    now: number,
  ): Promise<SessionCacheWriteOutcome> {
    const outcome = await this.#readwrite(async (tx) => {
      const result = await body(tx);
      this.#beforeCommit?.(operation, tx);
      return result;
    }, now);
    return outcome ?? { outcome: "failed" };
  }

  async #runTransaction<T>(
    stores: string[],
    mode: IDBTransactionMode,
    body: (tx: IDBTransaction) => Promise<T>,
  ): Promise<T> {
    const database = await this.#connection.open();
    return this.#onDatabaseTransaction(database, stores, mode, body);
  }

  async #onDatabaseTransaction<T>(
    database: IDBDatabase,
    stores: string[],
    mode: IDBTransactionMode,
    body: (tx: IDBTransaction) => Promise<T>,
  ): Promise<T> {
    const tx = database.transaction(stores, mode);
    const work = body(tx);
    const completion = transactionCompletion(tx, "abort");
    let timedOut = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const deadline = new Promise<never>((_resolve, reject) => {
      timer = setTimeout(() => {
        timedOut = true;
        reject(new Error("session cache transaction timed out"));
      }, STORAGE_WAIT_MS);
    });
    // Success requires both the body's result and the durable commit event
    // (requests issued; auto-commit happens when the microtask queue drains).
    // Promise.all rejects on either failure without waiting for the other, and
    // the catch aborts the transaction: a failed body - the beforeCommit fault
    // seam's throw - must not leave its already-issued requests committing
    // behind it, the same rollback the outbox's runner enforces.
    try {
      const [result] = await Promise.race([Promise.all([work, completion]), deadline]);
      return result;
    } catch (error) {
      tryAbortTransaction(tx);
      // Only ordinary transactions run here, never schema upgrades. A late
      // terminal event cannot settle the abandoned race or announce success.
      if (timedOut) this.#connection.retire(database);
      throw error;
    } finally {
      clearTimeout(timer);
    }
  }

  #reportOpenDiagnostic(path: SessionCacheOpenDiagnosticPath, versionchangeTransaction: boolean): void {
    try {
      this.#onOpenDiagnostic({
        database: this.#databaseName,
        version: this.#databaseVersion,
        path,
        versionchangeTransaction,
      });
    } catch {
      // A throwing reporter cannot change the storage outcome.
    }
  }

  #upgrade(database: IDBDatabase): void {
    upgradeSessionCache(database);
  }
}

function decodeCurrentRecord(value: unknown): CurrentSessionRecord | undefined {
  if (!isObject(value)) return undefined;
  if (typeof value.ownerToken !== "string" || typeof value.ref !== "string") return undefined;
  if (
    ![
      value.connectionGeneration,
      value.ownershipGeneration,
      value.clearEpoch,
      value.appliedSequence,
      value.savedAt,
    ].every(Number.isFinite)
  )
    return undefined;
  const state = decodeCurrentTranscriptState(value.state);
  if (state === undefined) return undefined;
  return { ...value, state } as unknown as CurrentSessionRecord;
}

function isCurrentMetaRow(value: unknown): value is CurrentMetaRow {
  return (
    isObject(value) &&
    typeof value.ownerToken === "string" &&
    typeof value.ref === "string" &&
    Number.isFinite(value.bytes) &&
    Number.isFinite(value.savedAt)
  );
}

function isCacheMetaRow(value: unknown): value is CacheMetaRow {
  return (
    isObject(value) && typeof value.ref === "string" && Number.isFinite(value.bytes) && Number.isFinite(value.savedAt)
  );
}
