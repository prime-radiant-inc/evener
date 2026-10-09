import {
  type CachedSessionItem,
  decodeTranscriptItem,
  encodeTranscriptItem,
  type ItemModel,
  mergeTranscriptItemVersioned,
  mergeTranscriptTurnVersioned,
  type ThreadItemPosition,
  type TurnModel,
} from "@evener/appwire-client";
import { IDBConnection, requestResult, transactionCompletion } from "./idbConnection";
import {
  ENTRY_COVERAGE_STORE,
  ENTRY_ITEM_STORE,
  ENTRY_META_STORE,
  ENTRY_TURN_STORE,
  LEGACY_META_STORE,
  SESSION_CACHE_DATABASE_NAME,
  type TranscriptAggregateMetaRow,
  type TranscriptCoverageRow,
  type TranscriptHistoryMetaRow,
  type TranscriptItemRow,
  type TranscriptTurnRow,
  upgradeSessionCache,
} from "./sessionCacheSchema";

export interface SessionEntryCacheOptions {
  databaseName: string;
  indexedDB?: IDBFactory;
  keyRange?: typeof IDBKeyRange;
}

export type EntryCoverageInput =
  | {
      kind: "read";
      readId: string;
      start: ThreadItemPosition | null;
      end: ThreadItemPosition | null;
      representedSnapshot: number;
      serverCursor?: string;
      boundary: string;
    }
  | {
      kind: "suffix";
      readId: string;
      continuityId: string;
      previousBoundary: string;
      boundary: string;
      start: ThreadItemPosition | null;
      end: ThreadItemPosition | null;
      representedSnapshot: number;
      serverCursor?: string;
    };

export interface EntryBatch {
  historyKey: string;
  clearEpoch: number;
  items: readonly TranscriptItemRow<unknown>[];
  turns?: readonly TurnModel[];
  coverage?: EntryCoverageInput;
}

export type EntryWriteOutcome =
  | { outcome: "committed"; coverage: "extended" | "unchanged" | "not-extended"; encodedBytes: number }
  | { outcome: "refused"; reason: "clear-epoch" }
  | { outcome: "failed"; error: unknown };

export interface EntryWindow {
  rows: TranscriptItemRow<unknown>[];
  coverage: TranscriptCoverageRow[];
  complete: boolean;
  stale: boolean;
  boundary?: string;
  serverCursor?: string;
}

const CLEAR_EPOCH_KEY = "__clearEpoch";
const AGGREGATE_META_KEY = "__aggregate";
const WRITE_STORES = [
  ENTRY_ITEM_STORE,
  ENTRY_TURN_STORE,
  ENTRY_COVERAGE_STORE,
  ENTRY_META_STORE,
  LEGACY_META_STORE,
] as const;

function encodedBytes(value: unknown): number {
  return new TextEncoder().encode(JSON.stringify(value)).byteLength;
}

function comparePosition(left: ThreadItemPosition | null, right: ThreadItemPosition | null): number {
  if (!left || !right) return left ? -1 : right ? 1 : 0;
  return left.entry - right.entry || left.item - right.item || (left.sub ?? 0) - (right.sub ?? 0);
}

function inRange(position: ThreadItemPosition, range: TranscriptCoverageRow): boolean {
  return (
    (!range.start || comparePosition(position, range.start) >= 0) &&
    (!range.end || comparePosition(position, range.end) <= 0)
  );
}

function turnMetadata(turn: TurnModel): Omit<TurnModel, "id" | "items"> {
  const { id: _id, items: _items, ...metadata } = turn;
  return metadata;
}

function turnModel(row: TranscriptTurnRow): TurnModel {
  return { id: row.turnId, ...row.metadata, items: [] };
}

/** Keyed transcript entry storage. Entry stores are enabled only in isolated fixture databases. */
export class SessionEntryCache {
  readonly #connection: IDBConnection;
  readonly #keyRange: typeof IDBKeyRange;

  constructor(options: SessionEntryCacheOptions) {
    if (options.databaseName === SESSION_CACHE_DATABASE_NAME) {
      throw new Error("entry storage requires an isolated fixture database");
    }
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

  /** Compatibility surface for the existing row-based repository; rows commit through the batch transaction. */
  async putItems<T>(rows: readonly TranscriptItemRow<T>[]): Promise<void> {
    const firstRow = rows[0];
    if (!firstRow) return;
    const result = await this.commitBatch({
      historyKey: firstRow.historyKey,
      clearEpoch: 0,
      items: rows as readonly TranscriptItemRow<unknown>[],
    });
    if (result.outcome !== "committed") {
      throw result.outcome === "failed" ? result.error : new Error("session entry write refused by clear epoch");
    }
  }

  async commitBatch(batch: EntryBatch): Promise<EntryWriteOutcome> {
    if (batch.items.some((row) => row.historyKey !== batch.historyKey)) {
      return { outcome: "failed", error: new Error("entry batch contains a different history identity") };
    }
    let transaction: IDBTransaction | undefined;
    let complete: Promise<void> | undefined;
    try {
      const database = await this.#connection.open();
      transaction = database.transaction([...WRITE_STORES], "readwrite");
      complete = transactionCompletion(transaction);
      const itemStore = transaction.objectStore(ENTRY_ITEM_STORE);
      const turnStore = transaction.objectStore(ENTRY_TURN_STORE);
      const coverageStore = transaction.objectStore(ENTRY_COVERAGE_STORE);
      const metaStore = transaction.objectStore(ENTRY_META_STORE);
      const durableEpoch =
        (
          await requestResult(
            transaction.objectStore(LEGACY_META_STORE).get(CLEAR_EPOCH_KEY) as IDBRequest<
              { epoch?: number } | undefined
            >,
          )
        )?.epoch ?? 0;
      if (durableEpoch !== batch.clearEpoch) {
        transaction.abort();
        await complete.catch(() => {});
        return { outcome: "refused", reason: "clear-epoch" };
      }

      const historyMeta = (await requestResult(
        metaStore.get(batch.historyKey) as IDBRequest<TranscriptHistoryMetaRow | undefined>,
      )) ?? { historyKey: batch.historyKey, encodedBytes: 0, itemCount: 0 };
      const aggregateMeta = (await requestResult(
        metaStore.get(AGGREGATE_META_KEY) as IDBRequest<TranscriptAggregateMetaRow | undefined>,
      )) ?? { historyKey: AGGREGATE_META_KEY, encodedBytes: 0 };
      let historyBytes = historyMeta.encodedBytes;
      let aggregateBytes = aggregateMeta.encodedBytes;
      let changedItems = 0;

      for (const row of batch.items) {
        const current = await requestResult(
          itemStore.get([row.historyKey, row.transcriptKey]) as IDBRequest<
            (TranscriptItemRow<CachedSessionItem> & { positionSub?: number }) | undefined
          >,
        );
        const cachedInput = row.item as Partial<CachedSessionItem> | null;
        const encodedInput =
          cachedInput && (cachedInput.identityOnly === true || cachedInput.textOmitted === true)
            ? (row.item as CachedSessionItem)
            : encodeTranscriptItem(row.item as ItemModel);
        const incoming = decodeTranscriptItem(encodedInput);
        if (!incoming) throw new Error("invalid transcript item for entry write");
        const held = current ? decodeTranscriptItem(current.item) : undefined;
        const merged: ItemModel = mergeTranscriptItemVersioned(held, current?.version, incoming, row.version);
        if (current && merged === held) continue;
        const encodedRow = {
          ...row,
          positionSub: row.position.sub ?? 0,
          item: encodeTranscriptItem(merged),
        };
        const oldBytes = current ? encodedBytes(current) : 0;
        const newBytes = encodedBytes(encodedRow);
        historyBytes += newBytes - oldBytes;
        aggregateBytes += newBytes - oldBytes;
        changedItems += current ? 0 : 1;
        itemStore.put(encodedRow);
      }

      for (const incoming of batch.turns ?? []) {
        const heldRow = await requestResult(
          turnStore.get([batch.historyKey, incoming.id]) as IDBRequest<TranscriptTurnRow | undefined>,
        );
        const held = heldRow ? turnModel(heldRow) : undefined;
        const merged = mergeTranscriptTurnVersioned(held, { ...incoming, items: [] });
        if (held && merged === held) continue;
        const row: TranscriptTurnRow = {
          historyKey: batch.historyKey,
          turnId: incoming.id,
          version: merged.version,
          metadata: turnMetadata(merged),
        };
        const oldBytes = heldRow ? encodedBytes(heldRow) : 0;
        const newBytes = encodedBytes(row);
        historyBytes += newBytes - oldBytes;
        aggregateBytes += newBytes - oldBytes;
        turnStore.put(row);
      }

      const coverageResult = await this.#applyCoverage(coverageStore, batch.historyKey, batch.coverage);
      historyBytes += coverageResult.byteDelta;
      aggregateBytes += coverageResult.byteDelta;
      metaStore.put({
        historyKey: batch.historyKey,
        encodedBytes: historyBytes,
        itemCount: historyMeta.itemCount + changedItems,
      });
      metaStore.put({ historyKey: AGGREGATE_META_KEY, encodedBytes: aggregateBytes });
      await complete;
      return { outcome: "committed", coverage: coverageResult.result, encodedBytes: aggregateBytes };
    } catch (error) {
      try {
        transaction?.abort();
      } catch {
        // The transaction may already have finished.
      }
      await complete?.catch(() => {});
      return { outcome: "failed", error };
    }
  }

  async #applyCoverage(
    store: IDBObjectStore,
    historyKey: string,
    input: EntryCoverageInput | undefined,
  ): Promise<{ result: "extended" | "unchanged" | "not-extended"; byteDelta: number }> {
    if (!input) return { result: "unchanged", byteDelta: 0 };
    if (!Number.isSafeInteger(input.representedSnapshot) || input.representedSnapshot < 0) {
      throw new Error("invalid represented transcript snapshot");
    }
    if (input.kind === "read") {
      const rangeId = `read:${input.readId}`;
      const key: IDBValidKey = [historyKey, rangeId];
      const current = await requestResult(store.get(key) as IDBRequest<TranscriptCoverageRow | undefined>);
      const row: TranscriptCoverageRow = {
        historyKey,
        rangeId,
        kind: "read",
        start: input.start,
        end: input.end,
        representedSnapshot: input.representedSnapshot,
        ...(input.serverCursor === undefined ? {} : { serverCursor: input.serverCursor }),
        readId: input.readId,
        boundary: input.boundary,
      };
      if (current && current.representedSnapshot >= row.representedSnapshot) {
        return { result: "unchanged", byteDelta: 0 };
      }
      store.put(row);
      return { result: "extended", byteDelta: encodedBytes(row) - (current ? encodedBytes(current) : 0) };
    }

    const baselineKey: IDBValidKey = [historyKey, `read:${input.readId}`];
    const baseline = await requestResult(store.get(baselineKey) as IDBRequest<TranscriptCoverageRow | undefined>);
    const suffixKey: IDBValidKey = [historyKey, `suffix:${input.readId}`];
    const previous = await requestResult(store.get(suffixKey) as IDBRequest<TranscriptCoverageRow | undefined>);
    const expectedBoundary = previous?.boundary ?? baseline?.boundary;
    const expectedContinuity = previous?.continuityId;
    if (
      !baseline ||
      baseline.suffixBlocked === true ||
      input.previousBoundary !== expectedBoundary ||
      (expectedContinuity !== undefined && expectedContinuity !== input.continuityId)
    ) {
      if (
        baseline &&
        baseline.suffixBlocked !== true &&
        (input.previousBoundary !== expectedBoundary ||
          (expectedContinuity !== undefined && expectedContinuity !== input.continuityId))
      ) {
        const blocked = { ...baseline, suffixBlocked: true };
        store.put(blocked);
        return { result: "not-extended", byteDelta: encodedBytes(blocked) - encodedBytes(baseline) };
      }
      return { result: "not-extended", byteDelta: 0 };
    }
    const row: TranscriptCoverageRow = {
      historyKey,
      rangeId: `suffix:${input.readId}`,
      kind: "suffix",
      start: baseline.start,
      end: input.end,
      representedSnapshot: input.representedSnapshot,
      ...(input.serverCursor === undefined ? {} : { serverCursor: input.serverCursor }),
      readId: input.readId,
      continuityId: input.continuityId,
      boundary: input.boundary,
    };
    if (previous && previous.representedSnapshot >= row.representedSnapshot) {
      return { result: "unchanged", byteDelta: 0 };
    }
    store.put(row);
    return { result: "extended", byteDelta: encodedBytes(row) - (previous ? encodedBytes(previous) : 0) };
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
      [historyKey, 0, 0, 0],
      [historyKey, Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER],
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
          const { positionSub: _positionSub, ...row } = cursor.value as TranscriptItemRow<T> & { positionSub?: number };
          rows.push(row as TranscriptItemRow<T>);
          if (rows.length === limit) {
            resolve();
            return;
          }
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

  async getLatestWindow(historyKey: string, limit: number, requestedBoundary: string): Promise<EntryWindow> {
    const rows = await this.getLatest(historyKey, limit);
    const database = await this.#connection.open();
    const transaction = database.transaction(ENTRY_COVERAGE_STORE, "readonly");
    const complete = transactionCompletion(transaction);
    const index = transaction.objectStore(ENTRY_COVERAGE_STORE).index("historyKey_boundary");
    const ranges = await new Promise<TranscriptCoverageRow[]>((resolve, reject) => {
      const output: TranscriptCoverageRow[] = [];
      const request = index.openCursor(this.#keyRange.only([historyKey, requestedBoundary]));
      request.addEventListener("error", () => reject(request.error ?? new Error("coverage cursor failed")), {
        once: true,
      });
      request.addEventListener("success", () => {
        const cursor = request.result;
        if (!cursor) {
          resolve(output);
          return;
        }
        output.push(cursor.value as TranscriptCoverageRow);
        cursor.continue();
      });
    });
    await complete;
    const candidates = ranges.filter((range) => range.kind === "read" || range.kind === "suffix");
    const best = candidates
      .filter((range) => range.boundary === requestedBoundary)
      .sort((left, right) => right.representedSnapshot - left.representedSnapshot)[0];
    const completeRange = best !== undefined && rows.every((row) => inRange(row.position, best));
    return {
      rows,
      coverage: best ? [best] : [],
      complete: completeRange,
      stale: !completeRange,
      ...(best
        ? { boundary: best.boundary, ...(best.serverCursor === undefined ? {} : { serverCursor: best.serverCursor }) }
        : {}),
    };
  }

  async getTurn(historyKey: string, turnId: string): Promise<TranscriptTurnRow | undefined> {
    const database = await this.#connection.open();
    const transaction = database.transaction(ENTRY_TURN_STORE, "readonly");
    const complete = transactionCompletion(transaction);
    const row = await requestResult(
      transaction.objectStore(ENTRY_TURN_STORE).get([historyKey, turnId]) as IDBRequest<TranscriptTurnRow | undefined>,
    );
    await complete;
    return row;
  }

  async getCoverage(historyKey: string, rangeId: string): Promise<TranscriptCoverageRow | undefined> {
    const database = await this.#connection.open();
    const transaction = database.transaction(ENTRY_COVERAGE_STORE, "readonly");
    const complete = transactionCompletion(transaction);
    const row = await requestResult(
      transaction.objectStore(ENTRY_COVERAGE_STORE).get([historyKey, rangeId]) as IDBRequest<
        TranscriptCoverageRow | undefined
      >,
    );
    await complete;
    return row;
  }

  async getByteTotals(historyKey: string): Promise<{ historyBytes: number; aggregateBytes: number }> {
    const database = await this.#connection.open();
    const transaction = database.transaction(ENTRY_META_STORE, "readonly");
    const complete = transactionCompletion(transaction);
    const store = transaction.objectStore(ENTRY_META_STORE);
    const [history, aggregate] = await Promise.all([
      requestResult(store.get(historyKey) as IDBRequest<TranscriptHistoryMetaRow | undefined>),
      requestResult(store.get(AGGREGATE_META_KEY) as IDBRequest<TranscriptAggregateMetaRow | undefined>),
    ]);
    await complete;
    return { historyBytes: history?.encodedBytes ?? 0, aggregateBytes: aggregate?.encodedBytes ?? 0 };
  }
}
