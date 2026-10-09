import {
  type CachedSessionItem,
  decodeTranscriptItem,
  encodeTranscriptItem,
  type ItemModel,
  mergeTranscriptItemVersioned,
} from "@evener/appwire-client";
import { IDBConnection, requestResult, transactionCompletion } from "./idbConnection";
import { ENTRY_ITEM_STORE, type TranscriptItemRow, upgradeSessionCache } from "./sessionCacheSchema";

export interface SessionEntryCacheOptions {
  databaseName: string;
  indexedDB?: IDBFactory;
  keyRange?: typeof IDBKeyRange;
}

/** Keyed transcript entry storage. Database selection is injectable for isolated tests. */
export class SessionEntryCache {
  readonly #connection: IDBConnection;
  readonly #keyRange: typeof IDBKeyRange;

  constructor(options: SessionEntryCacheOptions) {
    this.#keyRange = options.keyRange ?? globalThis.IDBKeyRange;
    this.#connection = new IDBConnection({
      indexedDB: options.indexedDB ?? globalThis.indexedDB,
      databaseName: options.databaseName,
      databaseVersion: 3,
      waitMs: 10_000,
      upgrade: (database) => upgradeSessionCache(database, true),
      errors: {
        open: "session entry cache open failed",
        superseded: "session entry cache open was superseded",
        timeout: () => new Error("session entry cache open timed out"),
      },
      reportDiagnostic: () => {},
    });
  }

  close(): void {
    this.#connection.close();
  }

  async putItems<T>(rows: readonly TranscriptItemRow<T>[]): Promise<void> {
    if (rows.length === 0) return;
    const database = await this.#connection.open();
    const transaction = database.transaction(ENTRY_ITEM_STORE, "readwrite");
    const complete = transactionCompletion(transaction);
    try {
      const store = transaction.objectStore(ENTRY_ITEM_STORE);
      for (const row of rows) {
        const current = await requestResult(
          store.get([row.historyKey, row.transcriptKey]) as IDBRequest<
            TranscriptItemRow<CachedSessionItem> | undefined
          >,
        );
        if (current && current.version > row.version) continue;
        const heldItem = current ? decodeTranscriptItem(current.item) : undefined;
        const incomingItem = decodeTranscriptItem(row.item);
        if (!incomingItem) throw new Error("invalid transcript item for entry write");
        const item: ItemModel = mergeTranscriptItemVersioned(heldItem, current?.version, incomingItem, row.version);
        store.put({ ...row, item: encodeTranscriptItem(item) });
      }
      await complete;
    } catch (error) {
      try {
        transaction.abort();
      } catch {
        // The transaction may already have finished.
      }
      await complete.catch(() => {});
      throw error;
    }
  }

  async getLatest<T>(historyKey: string, limit: number): Promise<TranscriptItemRow<T>[]> {
    if (!Number.isInteger(limit) || limit < 0)
      throw new RangeError("entry window limit must be a non-negative integer");
    if (limit === 0) return [];
    const database = await this.#connection.open();
    const transaction = database.transaction(ENTRY_ITEM_STORE, "readonly");
    const complete = transactionCompletion(transaction);
    const index = transaction.objectStore(ENTRY_ITEM_STORE).index("historyKey_position");
    const range = this.#keyRange.bound(
      [historyKey, 0, 0],
      [historyKey, Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER],
    );
    const rows: TranscriptItemRow<T>[] = [];
    try {
      await new Promise<void>((resolve, reject) => {
        const request = index.openCursor(range, "prev");
        request.addEventListener("error", () => reject(request.error ?? new Error("entry cursor failed")), {
          once: true,
        });
        request.addEventListener("success", () => {
          const cursor = request.result;
          if (!cursor || rows.length === limit) {
            resolve();
            return;
          }
          rows.push(cursor.value as TranscriptItemRow<T>);
          cursor.continue();
        });
      });
      await complete;
    } catch (error) {
      await complete.catch(() => {});
      throw error;
    }
    return rows.reverse();
  }
}
