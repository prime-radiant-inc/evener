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
  ENTRY_COVERAGE_BOUNDARY_SNAPSHOT_INDEX,
  ENTRY_COVERAGE_STORE,
  ENTRY_ITEM_STORE,
  ENTRY_META_STORE,
  ENTRY_TURN_STORE,
  entryHistoryKey,
  LEGACY_META_STORE,
  SESSION_CACHE_DATABASE_NAME,
  type TranscriptAggregateMetaRow,
  type TranscriptCoverageRow,
  type TranscriptHistoryMetaRow,
  type TranscriptItemRow,
  type TranscriptSessionMetadata,
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
    }
  | {
      // One independently persisted older-page certificate. `boundary` is the
      // page's unique identity (rangeId `page:<readId>:<boundary>`), so a retry
      // updates in place and a later read never erases it. `before` is the
      // captured exclusive request boundary and is provenance only.
      kind: "page";
      readId: string;
      before: ThreadItemPosition | null;
      start: ThreadItemPosition | null;
      end: ThreadItemPosition | null;
      representedSnapshot: number;
      serverCursor?: string;
      boundary: string;
    };

export interface EntryBatch {
  historyKey: string;
  clearEpoch: number;
  items: readonly TranscriptItemRow<unknown>[];
  turns?: readonly TurnModel[];
  coverage?: EntryCoverageInput;
  sessionMetadata?: TranscriptSessionMetadata;
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

type SessionMetadataPayload = Pick<
  TranscriptHistoryMetaRow,
  "historyKey" | "requestedRefs" | "resolvedIdentity" | "savedAt" | "accessedAt" | "formatVersion"
>;

function sessionMetadataPayload(
  row: TranscriptHistoryMetaRow | TranscriptSessionMetadata,
): SessionMetadataPayload | undefined {
  const { historyKey, requestedRefs, resolvedIdentity, savedAt, accessedAt, formatVersion } = row;
  if (
    requestedRefs === undefined &&
    resolvedIdentity === undefined &&
    savedAt === undefined &&
    accessedAt === undefined &&
    formatVersion === undefined
  ) {
    return undefined;
  }
  return { historyKey, requestedRefs, resolvedIdentity, savedAt, accessedAt, formatVersion };
}

function comparePosition(left: ThreadItemPosition | null, right: ThreadItemPosition | null): number {
  if (!left || !right) return left ? -1 : right ? 1 : 0;
  return left.entry - right.entry || left.item - right.item || (left.sub ?? 0) - (right.sub ?? 0);
}

function isPosition(value: ThreadItemPosition | null): value is ThreadItemPosition {
  return (
    value !== null &&
    Number.isSafeInteger(value.entry) &&
    value.entry >= 0 &&
    Number.isSafeInteger(value.item) &&
    value.item >= 0 &&
    (value.sub === undefined || (Number.isSafeInteger(value.sub) && value.sub >= 0))
  );
}

function positionKey(position: ThreadItemPosition): [number, number, number] {
  return [position.entry, position.item, position.sub ?? 0];
}

function compareNullablePosition(left: ThreadItemPosition | null, right: ThreadItemPosition | null): number {
  if (left === null && right === null) return 0;
  if (left === null) return -1;
  if (right === null) return 1;
  return comparePosition(left, right);
}

function positionCoveredBy(position: ThreadItemPosition, cert: TranscriptCoverageRow): boolean {
  if (cert.start === null || cert.end === null) return false;
  return comparePosition(cert.start, position) <= 0 && comparePosition(position, cert.end) <= 0;
}

function certsConnected(left: TranscriptCoverageRow, right: TranscriptCoverageRow): boolean {
  if (left.rangeId === right.rangeId) return true;
  if (
    left.start !== null &&
    left.end !== null &&
    right.start !== null &&
    right.end !== null &&
    comparePosition(left.start, right.end) <= 0 &&
    comparePosition(right.start, left.end) <= 0
  ) {
    return true;
  }
  // Provenance joins, not numeric adjacency: a page fetched against the
  // boundary of another certified range continues it, and that boundary is
  // recorded explicitly rather than inferred from neighbouring positions.
  if (left.before !== null && left.before !== undefined && positionCoveredBy(left.before, right)) return true;
  if (right.before !== null && right.before !== undefined && positionCoveredBy(right.before, left)) return true;
  return false;
}

// The connected certified component anchored at `anchor`: every certificate
// reachable through range overlap or recorded page provenance. Later reads
// never erase a page certificate, so this walks the full certified older
// history below the requested boundary within one query.
function connectedCoverage(
  ranges: readonly TranscriptCoverageRow[],
  anchor: TranscriptCoverageRow,
): TranscriptCoverageRow[] {
  const component = [anchor];
  const seen = new Set([anchor.rangeId]);
  let added = true;
  while (added) {
    added = false;
    for (const candidate of ranges) {
      if (seen.has(candidate.rangeId)) continue;
      if (!component.some((member) => certsConnected(candidate, member))) continue;
      component.push(candidate);
      seen.add(candidate.rangeId);
      added = true;
    }
  }
  return component;
}

// The cursor that continues an exhausted older window: the connected
// certificate reaching deepest. Each certificate's depth is its lowest known
// position, whether a certified range start/end or the boundary an empty page
// probed below. A probe below the same depth supersedes the range above it, so
// an empty page fetched against the deepest start wins over that range.
function chooseFrontierCursor(chain: readonly TranscriptCoverageRow[]): string | undefined {
  let best: TranscriptCoverageRow | undefined;
  let bestBottom: ThreadItemPosition | undefined;
  let bestProbe = false;
  for (const cert of chain) {
    const points: Array<{ point: ThreadItemPosition; probe: boolean }> = [];
    if (cert.start !== null) points.push({ point: cert.start, probe: false });
    if (cert.end !== null) points.push({ point: cert.end, probe: false });
    if (cert.before !== null && cert.before !== undefined) points.push({ point: cert.before, probe: true });
    const first = points[0];
    if (!first) continue;
    let bottom = first;
    for (const candidate of points) {
      if (comparePosition(candidate.point, bottom.point) < 0) bottom = candidate;
    }
    const deeper = bestBottom === undefined || comparePosition(bottom.point, bestBottom) < 0;
    const sameDepth = bestBottom !== undefined && comparePosition(bottom.point, bestBottom) === 0;
    const replace =
      deeper ||
      (sameDepth &&
        ((bottom.probe && !bestProbe) ||
          (bottom.probe === bestProbe && best !== undefined && cert.rangeId < best.rangeId)));
    if (replace) {
      best = cert;
      bestBottom = bottom.point;
      bestProbe = bottom.probe;
    }
  }
  return best?.serverCursor;
}

// The anchor certificate for an older-window query. Anchoring uses exact
// position equality and range containment, never numeric adjacency: two
// ranges whose endpoints merely follow one another numerically are not
// stitched, so an uncertified gap stays visibly uncertified.
function chooseOlderAnchor(
  ranges: readonly TranscriptCoverageRow[],
  before: ThreadItemPosition,
): TranscriptCoverageRow | undefined {
  const pickMostSpecific = (candidates: readonly TranscriptCoverageRow[]): TranscriptCoverageRow | undefined => {
    let best: TranscriptCoverageRow | undefined;
    for (const candidate of candidates) {
      if (!best) {
        best = candidate;
        continue;
      }
      const startDiff = compareNullablePosition(candidate.start, best.start);
      const endDiff = compareNullablePosition(candidate.end, best.end);
      if (
        startDiff > 0 ||
        (startDiff === 0 && endDiff > 0) ||
        (startDiff === 0 &&
          endDiff === 0 &&
          (candidate.representedSnapshot > best.representedSnapshot ||
            (candidate.representedSnapshot === best.representedSnapshot && candidate.rangeId < best.rangeId)))
      ) {
        best = candidate;
      }
    }
    return best;
  };
  const exactPages = ranges.filter(
    (row) =>
      row.kind === "page" &&
      row.before !== null &&
      row.before !== undefined &&
      comparePosition(row.before, before) === 0,
  );
  if (exactPages.length > 0) return pickMostSpecific(exactPages);
  const containing = ranges.filter(
    (row) =>
      row.start !== null &&
      row.end !== null &&
      comparePosition(row.start, before) <= 0 &&
      comparePosition(before, row.end) <= 0,
  );
  return pickMostSpecific(containing);
}

// Newest stored rows strictly below `before` accepted by `accept`, ascending.
// Scans descending and stops at `limit` accepted rows rather than loading the
// whole history; uncertified rows in the scan are skipped, never returned.
function acceptedRowsDescending<T>(
  index: IDBIndex,
  range: IDBKeyRange,
  limit: number,
  accept: (row: TranscriptItemRow<T>) => boolean,
): Promise<TranscriptItemRow<T>[]> {
  if (limit === 0) return Promise.resolve([]);
  const rows: TranscriptItemRow<T>[] = [];
  return new Promise((resolve, reject) => {
    const request = index.openCursor(range, "prev");
    request.addEventListener("error", () => reject(request.error ?? new Error("entry cursor failed")), { once: true });
    request.addEventListener("success", () => {
      const cursor = request.result;
      if (!cursor) {
        resolve(rows.reverse());
        return;
      }
      const { positionSub: _positionSub, ...row } = cursor.value as TranscriptItemRow<T> & { positionSub?: number };
      if (accept(row as TranscriptItemRow<T>)) {
        rows.push(row as TranscriptItemRow<T>);
        if (rows.length === limit) {
          resolve(rows.reverse());
          return;
        }
      }
      cursor.continue();
    });
  });
}

function itemIndexRange(
  keyRange: typeof IDBKeyRange,
  historyKey: string,
  coverage?: TranscriptCoverageRow,
): IDBKeyRange | null {
  if (coverage && coverage.start === null && coverage.end === null) return null;
  const lower: IDBValidKey = coverage?.start
    ? [historyKey, coverage.start.entry, coverage.start.item, coverage.start.sub ?? 0]
    : [historyKey, 0, 0, 0];
  const upper: IDBValidKey = coverage?.end
    ? [historyKey, coverage.end.entry, coverage.end.item, coverage.end.sub ?? 0]
    : [historyKey, Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER];
  return keyRange.bound(lower, upper);
}

function latestRowsFromIndex<T>(index: IDBIndex, range: IDBKeyRange, limit: number): Promise<TranscriptItemRow<T>[]> {
  if (limit === 0) return Promise.resolve([]);
  const rows: TranscriptItemRow<T>[] = [];
  return new Promise((resolve, reject) => {
    const request = index.openCursor(range, "prev");
    request.addEventListener("error", () => reject(request.error ?? new Error("entry cursor failed")), { once: true });
    request.addEventListener("success", () => {
      const cursor = request.result;
      if (!cursor || rows.length === limit) {
        resolve(rows.reverse());
        return;
      }
      const { positionSub: _positionSub, ...row } = cursor.value as TranscriptItemRow<T> & { positionSub?: number };
      rows.push(row as TranscriptItemRow<T>);
      if (rows.length === limit) {
        resolve(rows.reverse());
        return;
      }
      cursor.continue();
    });
  });
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
    if (
      batch.sessionMetadata &&
      (batch.sessionMetadata.historyKey !== batch.historyKey ||
        entryHistoryKey(batch.sessionMetadata.resolvedIdentity) !== batch.historyKey)
    ) {
      return { outcome: "failed", error: new Error("entry metadata does not match its resolved history identity") };
    }
    let transaction: IDBTransaction | undefined;
    let complete: Promise<void> | undefined;
    try {
      const database = await this.#connection.open();
      transaction = database.transaction([...WRITE_STORES], "readwrite");
      complete = transactionCompletion(transaction);
      void complete.catch(() => {});
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
      if (batch.sessionMetadata) {
        const previousPayload = sessionMetadataPayload(historyMeta);
        const nextPayload = sessionMetadataPayload(batch.sessionMetadata);
        const previousBytes = previousPayload ? encodedBytes(previousPayload) : 0;
        const nextBytes = nextPayload ? encodedBytes(nextPayload) : 0;
        historyBytes += nextBytes - previousBytes;
        aggregateBytes += nextBytes - previousBytes;
      }
      metaStore.put({
        ...historyMeta,
        ...batch.sessionMetadata,
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

    if (input.kind === "page") {
      if (input.before !== null && !isPosition(input.before)) throw new Error("invalid page request boundary");
      if (input.start !== null && !isPosition(input.start)) throw new Error("invalid page start position");
      if (input.end !== null && !isPosition(input.end)) throw new Error("invalid page end position");
      const rangeId = `page:${input.readId}:${input.boundary}`;
      const key: IDBValidKey = [historyKey, rangeId];
      const current = await requestResult(store.get(key) as IDBRequest<TranscriptCoverageRow | undefined>);
      const row: TranscriptCoverageRow = {
        historyKey,
        rangeId,
        kind: "page",
        start: input.start,
        end: input.end,
        before: input.before,
        representedSnapshot: input.representedSnapshot,
        ...(input.serverCursor === undefined ? {} : { serverCursor: input.serverCursor }),
        readId: input.readId,
        boundary: input.boundary,
      };
      if (
        current &&
        current.kind === "page" &&
        comparePosition(current.start, row.start) === 0 &&
        comparePosition(current.end, row.end) === 0 &&
        comparePosition(current.before ?? null, row.before ?? null) === 0 &&
        current.representedSnapshot === row.representedSnapshot &&
        current.serverCursor === row.serverCursor
      ) {
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
    if (previous && previous.representedSnapshot > row.representedSnapshot) {
      return { result: "unchanged", byteDelta: 0 };
    }
    if (
      previous &&
      previous.representedSnapshot === row.representedSnapshot &&
      previous.boundary === row.boundary &&
      comparePosition(previous.start, row.start) === 0 &&
      comparePosition(previous.end, row.end) === 0 &&
      previous.serverCursor === row.serverCursor
    ) {
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
    try {
      const rows = await latestRowsFromIndex<T>(index, range, limit);
      await complete;
      return rows;
    } catch (error) {
      await complete.catch(() => {});
      throw error;
    }
  }

  async getLatestWindow(historyKey: string, limit: number, requestedBoundary?: string): Promise<EntryWindow> {
    if (!Number.isInteger(limit) || limit < 0)
      throw new RangeError("entry window limit must be a non-negative integer");
    const database = await this.#connection.open();
    const transaction = database.transaction([ENTRY_ITEM_STORE, ENTRY_COVERAGE_STORE], "readonly");
    const complete = transactionCompletion(transaction);
    const itemIndex = transaction.objectStore(ENTRY_ITEM_STORE).index("historyKey_position");
    const coverageStore = transaction.objectStore(ENTRY_COVERAGE_STORE);
    const coverageIndex = coverageStore.index(ENTRY_COVERAGE_BOUNDARY_SNAPSHOT_INDEX);
    try {
      const best = await new Promise<TranscriptCoverageRow | undefined>((resolve, reject) => {
        if (requestedBoundary === undefined) {
          const request = coverageStore.openCursor(this.#keyRange.bound([historyKey, ""], [historyKey, "\uffff"]));
          let selected: TranscriptCoverageRow | undefined;
          request.addEventListener("error", () => reject(request.error ?? new Error("coverage cursor failed")), {
            once: true,
          });
          request.addEventListener("success", () => {
            const cursor = request.result;
            if (!cursor) {
              resolve(selected);
              return;
            }
            const row = cursor.value as TranscriptCoverageRow;
            // Page certificates are independent older ranges; they never
            // describe the latest window and must not hijack its selection.
            if (row.kind !== "page" && (!selected || row.representedSnapshot >= selected.representedSnapshot))
              selected = row;
            cursor.continue();
          });
          return;
        }
        const certificateRange = this.#keyRange.bound(
          [historyKey, requestedBoundary, 0],
          [historyKey, requestedBoundary, Number.MAX_SAFE_INTEGER],
        );
        const request = coverageIndex.openCursor(certificateRange, "prev");
        request.addEventListener("error", () => reject(request.error ?? new Error("coverage cursor failed")), {
          once: true,
        });
        request.addEventListener("success", () => {
          const cursor = request.result;
          if (!cursor) {
            resolve(undefined);
            return;
          }
          const row = cursor.value as TranscriptCoverageRow;
          if (row.kind === "page") {
            cursor.continue();
            return;
          }
          resolve(row);
        });
      });
      let coverage = best ? [best] : [];
      if (best?.kind === "suffix" && best.readId !== undefined) {
        const baseline = await requestResult(
          coverageStore.get([historyKey, `read:${best.readId}`]) as IDBRequest<TranscriptCoverageRow | undefined>,
        );
        if (baseline) coverage = [baseline, best];
      }
      const range = itemIndexRange(this.#keyRange, historyKey, best);
      const rows = range ? await latestRowsFromIndex<unknown>(itemIndex, range, limit) : [];
      await complete;
      return {
        rows,
        coverage,
        complete: best !== undefined,
        stale: best === undefined,
        ...(best
          ? {
              boundary: best.boundary,
              ...(best.kind === "read" && best.serverCursor !== undefined ? { serverCursor: best.serverCursor } : {}),
            }
          : {}),
      };
    } catch (error) {
      await complete.catch(() => {});
      throw error;
    }
  }

  /**
   * The newest stored rows strictly below `before`, ascending, drawn only from
   * certificates connected to the requested boundary by exact provenance or
   * range overlap (never numeric adjacency). Coverage authority and rows are
   * read in one readonly transaction.
   *
   * `serverCursor` is returned ONLY when `complete` is true: then the connected
   * certified coverage below the returned rows is exhausted and the cursor may
   * continue on the network. When `complete` is false the query was
   * limit-truncated; continue locally with `before = rows[0].position` and do
   * not hand any cursor to the network, so certified local rows are never
   * skipped. A requested boundary with no connected certificate returns
   * `stale: true` rather than stitching across an uncertified gap.
   */
  async getOlderWindow(historyKey: string, before: ThreadItemPosition, limit: number): Promise<EntryWindow> {
    if (!isPosition(before)) throw new RangeError("entry older window requires a real boundary position");
    if (!Number.isInteger(limit) || limit < 0)
      throw new RangeError("entry window limit must be a non-negative integer");
    const database = await this.#connection.open();
    const transaction = database.transaction([ENTRY_ITEM_STORE, ENTRY_COVERAGE_STORE], "readonly");
    const complete = transactionCompletion(transaction);
    try {
      const coverageStore = transaction.objectStore(ENTRY_COVERAGE_STORE);
      const itemIndex = transaction.objectStore(ENTRY_ITEM_STORE).index("historyKey_position");
      const ranges = await requestResult(
        coverageStore.getAll(this.#keyRange.bound([historyKey, ""], [historyKey, "\uffff"])) as IDBRequest<
          TranscriptCoverageRow[]
        >,
      );
      const anchor = chooseOlderAnchor(ranges, before);
      if (!anchor) {
        await complete;
        return { rows: [], coverage: [], complete: false, stale: true };
      }
      const chain = connectedCoverage(ranges, anchor);
      let oldestStart: ThreadItemPosition | null = null;
      for (const cert of chain) {
        if (cert.start === null || cert.end === null) continue;
        if (oldestStart === null || comparePosition(cert.start, oldestStart) < 0) oldestStart = cert.start;
      }
      const certified = oldestStart !== null;
      let rows: TranscriptItemRow<unknown>[] = [];
      // A boundary equal to (or below) the oldest certified start has no rows
      // strictly below it, and a half-open range with equal bounds is invalid.
      if (oldestStart !== null && limit > 0 && comparePosition(oldestStart, before) < 0) {
        const range = this.#keyRange.bound(
          [historyKey, ...positionKey(oldestStart)],
          [historyKey, ...positionKey(before)],
          false,
          true,
        );
        rows = await acceptedRowsDescending<unknown>(itemIndex, range, limit, (row) =>
          chain.some((cert) => positionCoveredBy(row.position, cert)),
        );
      }
      await complete;
      const emptyChain = !certified;
      const reachedBottom =
        rows.length < limit ||
        (oldestStart !== null && rows.length > 0 && comparePosition(rows[0]?.position ?? null, oldestStart) === 0);
      const isComplete = limit === 0 ? emptyChain : emptyChain || reachedBottom;
      // The continuation cursor belongs to the connected certificate that
      // reaches deepest. An empty page fetched against the deepest boundary is
      // that certificate even when it names no positions: it supersedes the
      // cursor of the range above it, and continues via its own cursor if any.
      const frontier = isComplete ? chooseFrontierCursor(chain) : undefined;
      return {
        rows,
        coverage: chain,
        complete: isComplete,
        stale: false,
        boundary: anchor.boundary,
        ...(frontier !== undefined ? { serverCursor: frontier } : {}),
      };
    } catch (error) {
      await complete.catch(() => {});
      throw error;
    }
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

  async getSessionMetadata(historyKey: string): Promise<TranscriptSessionMetadata | undefined> {
    const database = await this.#connection.open();
    const transaction = database.transaction(ENTRY_META_STORE, "readonly");
    const complete = transactionCompletion(transaction);
    const row = await requestResult(
      transaction.objectStore(ENTRY_META_STORE).get(historyKey) as IDBRequest<TranscriptHistoryMetaRow | undefined>,
    );
    await complete;
    if (
      !row ||
      row.requestedRefs === undefined ||
      row.resolvedIdentity === undefined ||
      row.savedAt === undefined ||
      row.accessedAt === undefined ||
      row.formatVersion === undefined
    ) {
      return undefined;
    }
    return {
      historyKey: row.historyKey,
      requestedRefs: row.requestedRefs,
      resolvedIdentity: row.resolvedIdentity,
      savedAt: row.savedAt,
      accessedAt: row.accessedAt,
      formatVersion: row.formatVersion,
    };
  }
}
