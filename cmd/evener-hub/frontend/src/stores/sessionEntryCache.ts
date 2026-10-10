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
  CURRENT_META_STORE,
  CURRENT_STORE,
  ENTRY_COVERAGE_BOUNDARY_SNAPSHOT_INDEX,
  ENTRY_COVERAGE_STORE,
  ENTRY_ITEM_STORE,
  ENTRY_META_STORE,
  ENTRY_TURN_STORE,
  entryHistoryKey,
  LEGACY_META_STORE,
  LEGACY_RECORDS_STORE,
  SESSION_CACHE_DATABASE_VERSION,
  type TranscriptAggregateMetaRow,
  type TranscriptCoverageRow,
  type TranscriptHistoryMetaRow,
  type TranscriptItemRow,
  type TranscriptRequestedRef,
  type TranscriptSessionMetadata,
  type TranscriptTurnRow,
  upgradeSessionCache,
} from "./sessionCacheSchema";

export interface SessionEntryCacheOptions {
  databaseName: string;
  indexedDB?: IDBFactory;
  keyRange?: typeof IDBKeyRange;
  // Aggregate encoded-byte cap. Defaults to the spec's 32 MiB disk policy; the
  // injection seam lets tests exercise eviction with tiny budgets.
  maxBytes?: number;
  // Retention window for a history identity's saved/accessed time. Defaults to
  // the spec's 14 days.
  ttlMs?: number;
  // Clock seam for TTL and LRU decisions, so tests never wait on real time.
  now?: () => number;
  // Preference seam. Eviction reclaims expired units first, then unpresferred
  // units, then preferred units only as needed; the cache invents no discovery.
  // Pure scalar/identity: no async work and no transcript bodies.
  isPreferred?: (unit: RetentionUnit) => boolean;
}

export type RetentionUnit =
  | { kind: "entry"; historyKey: string }
  | { kind: "records"; ref: string }
  | { kind: "current"; ownerToken: string; ref: string };

export interface EntryPolicyResult {
  aggregateBytes: number;
  protectedSurvived: boolean;
  overCapacity: boolean;
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
      // The opaque request cursor this page consumed, when it answered a
      // cursor request rather than a position-only request. It is the
      // successor edge to the certificate whose `serverCursor` it equals.
      requestCursor?: string;
      // Opaque equality token for the source authority that owns the chain
      // this certificate belongs to (the repository binding lifetime).
      authority?: string;
      boundary: string;
    };

export interface EntryBatch {
  historyKey: string;
  clearEpoch: number;
  // Durable history generation the caller read for this identity. A batch
  // captured before a deleteHistory of the same identity carries the old value
  // and is refused.
  historyGeneration?: number;
  items: readonly TranscriptItemRow<unknown>[];
  turns?: readonly TurnModel[];
  coverage?: EntryCoverageInput;
  sessionMetadata?: TranscriptSessionMetadata;
}

export type EntryWriteOutcome =
  | { outcome: "committed"; coverage: "extended" | "unchanged" | "not-extended"; encodedBytes: number }
  | { outcome: "refused"; reason: "clear-epoch" | "history-fence" | "capacity" }
  | { outcome: "failed"; error: unknown };

export interface EntryWindow {
  rows: TranscriptItemRow<unknown>[];
  coverage: TranscriptCoverageRow[];
  complete: boolean;
  stale: boolean;
  boundary?: string;
  serverCursor?: string;
}

export type EntryMigrationSource =
  | { kind: "records"; ref: string }
  | { kind: "current"; key: readonly [string, string] };

export interface EntryMigrationRequest {
  historyKey: string;
  clearEpoch: number;
  historyGeneration?: number;
  source: EntryMigrationSource;
  sessionMetadata?: TranscriptSessionMetadata;
  // Pure conversion of the stored legacy value into keyed entry rows. Returning
  // undefined marks the record undecodable and leaves its source intact.
  convert: (
    legacy: unknown,
  ) => { items: readonly TranscriptItemRow<unknown>[]; turns?: readonly TurnModel[] } | undefined;
}

export type EntryMigrationOutcome =
  | { outcome: "converted"; itemCount: number; turnCount: number; encodedBytes: number; removedBytes: number }
  | { outcome: "skipped"; reason: "missing" | "undecodable" }
  | { outcome: "refused"; reason: "clear-epoch" | "history-fence" | "capacity" }
  | { outcome: "failed"; error: unknown };

const CLEAR_EPOCH_KEY = "__clearEpoch";
const AGGREGATE_META_KEY = "__aggregate";
const DEFAULT_MAX_BYTES = 32 * 1024 * 1024;
const DEFAULT_TTL_MS = 14 * 24 * 60 * 60 * 1000;
const WRITE_STORES = [
  ENTRY_ITEM_STORE,
  ENTRY_TURN_STORE,
  ENTRY_COVERAGE_STORE,
  ENTRY_META_STORE,
  LEGACY_RECORDS_STORE,
  LEGACY_META_STORE,
  CURRENT_STORE,
  CURRENT_META_STORE,
] as const;
const MIGRATION_STORES = WRITE_STORES;
const CLEAR_STORES = [
  ENTRY_ITEM_STORE,
  ENTRY_TURN_STORE,
  ENTRY_COVERAGE_STORE,
  ENTRY_META_STORE,
  LEGACY_RECORDS_STORE,
  LEGACY_META_STORE,
  CURRENT_STORE,
  CURRENT_META_STORE,
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

// Migration merges retention metadata instead of replacing it: a compatible
// legacy source must never regress a newer history's timestamps or drop its
// aliases, because the TTL policy reads those timestamps in the same
// transaction and could otherwise delete the freshly merged newer content.
function mergeRequestedRefs(
  previous: readonly TranscriptRequestedRef[] | undefined,
  incoming: readonly TranscriptRequestedRef[],
): TranscriptRequestedRef[] {
  const byKey = new Map<string, TranscriptRequestedRef>();
  for (const ref of previous ?? []) byKey.set(`${ref.clientId}\u0000${ref.ref}`, ref);
  for (const ref of incoming) byKey.set(`${ref.clientId}\u0000${ref.ref}`, ref);
  return [...byKey.values()];
}

function mergeSessionMetadata(
  previous: TranscriptHistoryMetaRow,
  incoming: TranscriptSessionMetadata,
): SessionMetadataPayload {
  return {
    historyKey: previous.historyKey,
    requestedRefs: mergeRequestedRefs(previous.requestedRefs, incoming.requestedRefs),
    resolvedIdentity: previous.resolvedIdentity ?? incoming.resolvedIdentity,
    savedAt: Math.max(previous.savedAt ?? 0, incoming.savedAt),
    accessedAt: Math.max(previous.accessedAt ?? 0, incoming.accessedAt),
    formatVersion: Math.max(previous.formatVersion ?? 0, incoming.formatVersion),
  };
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

function isProtectedUnit(unit: RetentionUnit, protect: RetentionUnit): boolean {
  if (unit.kind !== protect.kind) return false;
  if (unit.kind === "entry" && protect.kind === "entry") return unit.historyKey === protect.historyKey;
  if (unit.kind === "records" && protect.kind === "records") return unit.ref === protect.ref;
  if (unit.kind === "current" && protect.kind === "current")
    return unit.ownerToken === protect.ownerToken && unit.ref === protect.ref;
  return false;
}

function unitSortKey(unit: RetentionUnit): string {
  if (unit.kind === "entry") return `entry\u0000${unit.historyKey}`;
  if (unit.kind === "records") return `records\u0000${unit.ref}`;
  return `current\u0000${unit.ownerToken}\u0000${unit.ref}`;
}

function positionCoveredBy(position: ThreadItemPosition, cert: TranscriptCoverageRow): boolean {
  if (cert.start === null || cert.end === null) return false;
  return comparePosition(cert.start, position) <= 0 && comparePosition(position, cert.end) <= 0;
}

function certsConnected(left: TranscriptCoverageRow, right: TranscriptCoverageRow): boolean {
  if (left.rangeId === right.rangeId) return true;
  // A certificate may only ever continue a chain owned by the same authority:
  // a replacement or reconnect that begins a fresh baseline must not have its
  // older pages stitched onto the outgoing chain, whatever their positions.
  if (left.authority !== right.authority) return false;
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
  // A recorded cursor succession is itself a continuity link: a page fetched
  // against another certificate's `serverCursor` continues it, even when their
  // ranges do not overlap. Without this an empty-only successor chain would
  // select its predecessor alone and never see the consumed edge.
  if (left.requestCursor !== undefined && left.requestCursor === right.serverCursor) return true;
  if (right.requestCursor !== undefined && right.requestCursor === left.serverCursor) return true;
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

// The deepest point a certificate reaches, whether a certified range boundary
// or the exclusive boundary an empty page probed below. A probe below a range
// at the same depth supersedes that range, because the empty page was fetched
// against it.
function certificateBottom(cert: TranscriptCoverageRow): { point: ThreadItemPosition; probe: boolean } | undefined {
  const points: Array<{ point: ThreadItemPosition; probe: boolean }> = [];
  if (cert.start !== null) points.push({ point: cert.start, probe: false });
  if (cert.end !== null) points.push({ point: cert.end, probe: false });
  if (cert.before !== null && cert.before !== undefined) points.push({ point: cert.before, probe: true });
  const first = points[0];
  if (!first) return undefined;
  let bottom = first;
  for (const candidate of points) {
    if (comparePosition(candidate.point, bottom.point) < 0) bottom = candidate;
  }
  return bottom;
}

// A page certificate that predates recorded cursor edges: it neither carried a
// request cursor nor was the target of one. A probe fetched against its range
// boundary supersedes it in the accepted deeper-range behavior, so it must not
// fabricate ambiguity against that probe. Certificates that participate in the
// cursor graph are never superseded here.
function legacySupersededByProbe(
  cert: TranscriptCoverageRow,
  probes: readonly TranscriptCoverageRow[],
  chain: readonly TranscriptCoverageRow[],
): boolean {
  // Only a certificate with no authority at all is the pre-authority legacy
  // format. A current position-only page explicitly omits its request cursor
  // but still carries an authority, so it must never be silently superseded.
  if (cert.authority !== undefined) return false;
  if (cert.requestCursor !== undefined) return false;
  if (chain.some((row) => row.requestCursor !== undefined && row.requestCursor === cert.serverCursor)) return false;
  const bottom = certificateBottom(cert);
  if (bottom?.probe === true) return false;
  return probes.some((probe) => {
    const before = probe.before;
    return before !== null && before !== undefined && positionCoveredBy(before, cert);
  });
}

// The frontier of an exhausted chain, resolved from recorded successor edges
// alone: cert S is the successor of P iff same authority, `S.requestCursor`
// is defined, `P.serverCursor` is defined and they are equal. No commit order,
// timestamp, lexeme, position or snapshot tie-break. Every unconsumed
// certificate at the deepest depth is a peer regardless of probe status; only a
// legacy certificate superseded by a same-depth probe is excluded. `verified`
// false means the chain is ambiguous (zero or several unconsumed certificates,
// or conflicting cursors), and uncertainty must never present as exhausted
// history.
function chooseFrontier(chain: readonly TranscriptCoverageRow[]): { cursor?: string; verified: boolean } {
  const consumed = new Set<string>();
  for (const predecessor of chain) {
    if (predecessor.serverCursor === undefined) continue;
    const successors = chain.filter(
      (successor) =>
        successor.rangeId !== predecessor.rangeId &&
        successor.authority === predecessor.authority &&
        successor.requestCursor !== undefined &&
        successor.requestCursor === predecessor.serverCursor,
    );
    if (successors.length === 0) continue;
    // Two readers that both consumed the same cursor but disagree on what
    // follows are a branch: the continuation is unverified, whatever the
    // depths of their successors — a deeper branch must not hide its peer.
    const distinct = new Set(successors.map((successor) => successor.serverCursor ?? "\u0000terminal"));
    if (distinct.size > 1) return { verified: false };
    consumed.add(predecessor.rangeId);
  }
  let deepestPoint: ThreadItemPosition | undefined;
  for (const cert of chain) {
    const bottom = certificateBottom(cert);
    if (!bottom) continue;
    if (deepestPoint === undefined || comparePosition(bottom.point, deepestPoint) < 0) {
      deepestPoint = bottom.point;
    }
  }
  if (deepestPoint === undefined) return { verified: true };
  const depth = deepestPoint;
  const atDepth = chain.filter((cert) => {
    const bottom = certificateBottom(cert);
    return bottom !== undefined && comparePosition(bottom.point, depth) === 0;
  });
  const probes = atDepth.filter((cert) => certificateBottom(cert)?.probe === true);
  const unconsumed = atDepth.filter(
    (cert) => !consumed.has(cert.rangeId) && !legacySupersededByProbe(cert, probes, chain),
  );
  const first = unconsumed[0];
  if (!first) return { verified: false };
  if (unconsumed.length === 1) {
    return first.serverCursor === undefined ? { verified: true } : { verified: true, cursor: first.serverCursor };
  }
  const consistent =
    first.serverCursor !== undefined &&
    unconsumed.every((cert) => cert.serverCursor !== undefined && cert.serverCursor === first.serverCursor);
  return consistent ? { verified: true, cursor: first.serverCursor } : { verified: false };
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

// Versioned upsert of keyed item/turn rows within one open transaction. Shared
// by the ordinary batch path and migration so both honour the same identity and
// richness rules. Returns the encoded-byte delta and the count of newly created
// items; a row that loses to its held version contributes nothing.
async function applyEntryRows(
  itemStore: IDBObjectStore,
  turnStore: IDBObjectStore,
  historyKey: string,
  items: readonly TranscriptItemRow<unknown>[],
  turns: readonly TurnModel[],
): Promise<{ byteDelta: number; changedItems: number }> {
  let byteDelta = 0;
  let changedItems = 0;
  for (const row of items) {
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
    const encodedRow = { ...row, positionSub: row.position.sub ?? 0, item: encodeTranscriptItem(merged) };
    const oldBytes = current ? encodedBytes(current) : 0;
    const newBytes = encodedBytes(encodedRow);
    byteDelta += newBytes - oldBytes;
    changedItems += current ? 0 : 1;
    itemStore.put(encodedRow);
  }
  for (const incoming of turns) {
    const heldRow = await requestResult(
      turnStore.get([historyKey, incoming.id]) as IDBRequest<TranscriptTurnRow | undefined>,
    );
    const held = heldRow ? turnModel(heldRow) : undefined;
    const merged = mergeTranscriptTurnVersioned(held, { ...incoming, items: [] });
    if (held && merged === held) continue;
    const row: TranscriptTurnRow = {
      historyKey,
      turnId: incoming.id,
      version: merged.version,
      metadata: turnMetadata(merged),
    };
    const oldBytes = heldRow ? encodedBytes(heldRow) : 0;
    const newBytes = encodedBytes(row);
    byteDelta += newBytes - oldBytes;
    turnStore.put(row);
  }
  return { byteDelta, changedItems };
}

/** Keyed transcript entry storage on the shared session-cache schema. */
export class SessionEntryCache {
  readonly #connection: IDBConnection;
  readonly #keyRange: typeof IDBKeyRange;
  readonly #maxBytes: number;
  readonly #ttlMs: number;
  readonly #now: () => number;
  readonly #isPreferred: ((unit: RetentionUnit) => boolean) | undefined;
  // Per-history demand preferences registered by the live owners of an
  // identity (the entry repositories). The cache invents no discovery: an
  // owner registers real Live/demand state, and the policy consults it only as
  // a fallback to the construction-time `isPreferred` seam.
  readonly #preferredHistories = new Map<string, () => boolean>();

  constructor(options: SessionEntryCacheOptions) {
    this.#keyRange = options.keyRange ?? globalThis.IDBKeyRange;
    this.#maxBytes = options.maxBytes ?? DEFAULT_MAX_BYTES;
    this.#ttlMs = options.ttlMs ?? DEFAULT_TTL_MS;
    this.#now = options.now ?? Date.now;
    this.#isPreferred = options.isPreferred;
    this.#connection = new IDBConnection({
      indexedDB: options.indexedDB ?? globalThis.indexedDB,
      databaseName: options.databaseName,
      databaseVersion: SESSION_CACHE_DATABASE_VERSION,
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

  /**
   * Register one live owner's real demand for its history identity as that
   * identity's retention preference. The returned function removes only this
   * registration, so a replacement owner cannot be unregistered by its
   * predecessor. Purely scalar: no async work and no transcript bodies.
   */
  registerPreferredHistory(historyKey: string, predicate: () => boolean): () => void {
    this.#preferredHistories.set(historyKey, predicate);
    return () => {
      if (this.#preferredHistories.get(historyKey) === predicate) this.#preferredHistories.delete(historyKey);
    };
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
      if ((historyMeta.generation ?? 0) !== (batch.historyGeneration ?? 0)) {
        transaction.abort();
        await complete.catch(() => {});
        return { outcome: "refused", reason: "history-fence" };
      }
      const aggregateMeta = (await requestResult(
        metaStore.get(AGGREGATE_META_KEY) as IDBRequest<TranscriptAggregateMetaRow | undefined>,
      )) ?? { historyKey: AGGREGATE_META_KEY, encodedBytes: 0 };
      let historyBytes = historyMeta.encodedBytes;
      let aggregateBytes = aggregateMeta.encodedBytes;
      const written = await applyEntryRows(itemStore, turnStore, batch.historyKey, batch.items, batch.turns ?? []);
      historyBytes += written.byteDelta;
      aggregateBytes += written.byteDelta;
      const changedItems = written.changedItems;

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
      const policy = await this.#enforcePolicy(transaction, { kind: "entry", historyKey: batch.historyKey });
      if (policy.overCapacity) {
        transaction.abort();
        await complete.catch(() => {});
        return { outcome: "refused", reason: "capacity" };
      }
      await complete;
      return {
        outcome: "committed",
        coverage: policy.protectedSurvived ? coverageResult.result : "not-extended",
        encodedBytes: policy.aggregateBytes,
      };
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

  /**
   * Convert one legacy record into entry rows and remove its source together
   * in one transaction. Interrupted or aborted conversion aborts before any
   * durable effect, so the source survives for retry. Records import as stale
   * seeds: items and item-free turns only, never overlay, currentness or
   * coverage. The durable Clear epoch fences the whole transaction.
   */
  async commitMigration(request: EntryMigrationRequest): Promise<EntryMigrationOutcome> {
    if (
      request.sessionMetadata &&
      (request.sessionMetadata.historyKey !== request.historyKey ||
        entryHistoryKey(request.sessionMetadata.resolvedIdentity) !== request.historyKey)
    ) {
      return { outcome: "failed", error: new Error("entry metadata does not match its resolved history identity") };
    }
    let transaction: IDBTransaction | undefined;
    let complete: Promise<void> | undefined;
    try {
      const database = await this.#connection.open();
      transaction = database.transaction([...MIGRATION_STORES], "readwrite");
      complete = transactionCompletion(transaction);
      void complete.catch(() => {});
      const legacyMetaStore = transaction.objectStore(LEGACY_META_STORE);
      const metaStore = transaction.objectStore(ENTRY_META_STORE);
      const itemStore = transaction.objectStore(ENTRY_ITEM_STORE);
      const turnStore = transaction.objectStore(ENTRY_TURN_STORE);
      const durableEpoch =
        (await requestResult(legacyMetaStore.get(CLEAR_EPOCH_KEY) as IDBRequest<{ epoch?: number } | undefined>))
          ?.epoch ?? 0;
      if (durableEpoch !== request.clearEpoch) {
        transaction.abort();
        await complete.catch(() => {});
        return { outcome: "refused", reason: "clear-epoch" };
      }

      const historyMeta = (await requestResult(
        metaStore.get(request.historyKey) as IDBRequest<TranscriptHistoryMetaRow | undefined>,
      )) ?? { historyKey: request.historyKey, encodedBytes: 0, itemCount: 0 };
      // Fence before touching the source: a migration captured before a delete
      // is refused without removing the legacy source it would have converted.
      if ((historyMeta.generation ?? 0) !== (request.historyGeneration ?? 0)) {
        transaction.abort();
        await complete.catch(() => {});
        return { outcome: "refused", reason: "history-fence" };
      }

      let legacy: unknown;
      let removedBytes = 0;
      if (request.source.kind === "records") {
        const record = await requestResult(transaction.objectStore(LEGACY_RECORDS_STORE).get(request.source.ref));
        const sourceMeta = await requestResult(legacyMetaStore.get(request.source.ref));
        if (record === undefined) {
          await complete;
          return { outcome: "skipped", reason: "missing" };
        }
        legacy = record;
        removedBytes = encodedBytes(record) + (sourceMeta === undefined ? 0 : encodedBytes(sourceMeta));
        transaction.objectStore(LEGACY_RECORDS_STORE).delete(request.source.ref);
        legacyMetaStore.delete(request.source.ref);
      } else {
        const key = [...request.source.key];
        const record = await requestResult(transaction.objectStore(CURRENT_STORE).get(key));
        const sourceMeta = await requestResult(transaction.objectStore(CURRENT_META_STORE).get(key));
        if (record === undefined) {
          await complete;
          return { outcome: "skipped", reason: "missing" };
        }
        legacy = record;
        removedBytes = encodedBytes(record) + (sourceMeta === undefined ? 0 : encodedBytes(sourceMeta));
        transaction.objectStore(CURRENT_STORE).delete(key);
        transaction.objectStore(CURRENT_META_STORE).delete(key);
      }

      const converted = request.convert(legacy);
      if (!converted) {
        transaction.abort();
        await complete.catch(() => {});
        return { outcome: "skipped", reason: "undecodable" };
      }
      if (converted.items.some((row) => row.historyKey !== request.historyKey)) {
        throw new Error("migration produced a different history identity");
      }

      const aggregateMeta = (await requestResult(
        metaStore.get(AGGREGATE_META_KEY) as IDBRequest<TranscriptAggregateMetaRow | undefined>,
      )) ?? { historyKey: AGGREGATE_META_KEY, encodedBytes: 0 };
      let historyBytes = historyMeta.encodedBytes;
      let aggregateBytes = aggregateMeta.encodedBytes;
      const written = await applyEntryRows(
        itemStore,
        turnStore,
        request.historyKey,
        converted.items,
        converted.turns ?? [],
      );
      historyBytes += written.byteDelta;
      aggregateBytes += written.byteDelta;
      const mergedMetadata = request.sessionMetadata
        ? mergeSessionMetadata(historyMeta, request.sessionMetadata)
        : undefined;
      if (mergedMetadata) {
        const previousPayload = sessionMetadataPayload(historyMeta);
        const nextPayload = mergedMetadata;
        const metadataDelta =
          (nextPayload ? encodedBytes(nextPayload) : 0) - (previousPayload ? encodedBytes(previousPayload) : 0);
        historyBytes += metadataDelta;
        aggregateBytes += metadataDelta;
      }
      metaStore.put({
        ...historyMeta,
        ...mergedMetadata,
        historyKey: request.historyKey,
        encodedBytes: historyBytes,
        itemCount: historyMeta.itemCount + written.changedItems,
      });
      metaStore.put({ historyKey: AGGREGATE_META_KEY, encodedBytes: aggregateBytes });
      const policy = await this.#enforcePolicy(transaction, { kind: "entry", historyKey: request.historyKey });
      if (policy.overCapacity) {
        transaction.abort();
        await complete.catch(() => {});
        return { outcome: "refused", reason: "capacity" };
      }
      await complete;
      return {
        outcome: "converted",
        itemCount: converted.items.length,
        turnCount: converted.turns?.length ?? 0,
        encodedBytes: policy.aggregateBytes,
        removedBytes,
      };
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

  /**
   * Atomically remove every entry row for one history identity and advance its
   * durable generation, leaving a fence tombstone with no timestamps so the TTL
   * and LRU policy can never reclaim it. Work captured before the delete carries
   * the old generation and is refused; explicit fresh caching reads the new
   * generation first. Returns true only when the transaction completed.
   */
  async deleteHistory(historyKey: string): Promise<boolean> {
    let transaction: IDBTransaction | undefined;
    let complete: Promise<void> | undefined;
    try {
      const database = await this.#connection.open();
      transaction = database.transaction([...WRITE_STORES], "readwrite");
      complete = transactionCompletion(transaction);
      void complete.catch(() => {});
      const metaStore = transaction.objectStore(ENTRY_META_STORE);
      const historyMeta = await requestResult(
        metaStore.get(historyKey) as IDBRequest<TranscriptHistoryMetaRow | undefined>,
      );
      const aggregateMeta = await requestResult(
        metaStore.get(AGGREGATE_META_KEY) as IDBRequest<TranscriptAggregateMetaRow | undefined>,
      );
      const removed = await this.#deleteHistoryRows(transaction, historyKey);
      metaStore.put({
        historyKey,
        encodedBytes: 0,
        itemCount: 0,
        generation: (historyMeta?.generation ?? 0) + 1,
      });
      metaStore.put({
        historyKey: AGGREGATE_META_KEY,
        encodedBytes: Math.max(0, (aggregateMeta?.encodedBytes ?? 0) - removed),
      });
      await complete;
      return true;
    } catch {
      try {
        transaction?.abort();
      } catch {
        // The transaction may already have finished.
      }
      await complete?.catch(() => {});
      return false;
    }
  }

  /** The durable generation a caller must carry to write this identity. */
  async getHistoryGeneration(historyKey: string): Promise<number> {
    const database = await this.#connection.open();
    const transaction = database.transaction(ENTRY_META_STORE, "readonly");
    const complete = transactionCompletion(transaction);
    try {
      const row = await requestResult(
        transaction.objectStore(ENTRY_META_STORE).get(historyKey) as IDBRequest<TranscriptHistoryMetaRow | undefined>,
      );
      await complete;
      return row?.generation ?? 0;
    } catch (error) {
      await complete.catch(() => {});
      throw error;
    }
  }

  /** The durable Clear epoch a caller must carry to write any history. */
  async getClearEpoch(): Promise<number> {
    const database = await this.#connection.open();
    const transaction = database.transaction(LEGACY_META_STORE, "readonly");
    const complete = transactionCompletion(transaction);
    try {
      const row = await requestResult(
        transaction.objectStore(LEGACY_META_STORE).get(CLEAR_EPOCH_KEY) as IDBRequest<{ epoch?: number } | undefined>,
      );
      await complete;
      return row?.epoch ?? 0;
    } catch (error) {
      await complete.catch(() => {});
      throw error;
    }
  }

  /**
   * Atomically remove every history record - entry stores, legacy records and
   * current checkpoints - and advance the shared durable Clear epoch by one in
   * the same transaction. The reserved epoch row in the legacy meta store is
   * the existing cross-tab fence. Abort leaves every store, generation tombstone
   * and the epoch unchanged.
   */
  async clear(): Promise<{ committed: boolean; epoch: number }> {
    let observed = 0;
    let transaction: IDBTransaction | undefined;
    let complete: Promise<void> | undefined;
    try {
      const database = await this.#connection.open();
      transaction = database.transaction([...CLEAR_STORES], "readwrite");
      complete = transactionCompletion(transaction);
      void complete.catch(() => {});
      const legacyMetaStore = transaction.objectStore(LEGACY_META_STORE);
      observed =
        (await requestResult(legacyMetaStore.get(CLEAR_EPOCH_KEY) as IDBRequest<{ epoch?: number } | undefined>))
          ?.epoch ?? 0;
      transaction.objectStore(ENTRY_ITEM_STORE).clear();
      transaction.objectStore(ENTRY_TURN_STORE).clear();
      transaction.objectStore(ENTRY_COVERAGE_STORE).clear();
      transaction.objectStore(ENTRY_META_STORE).clear();
      transaction.objectStore(LEGACY_RECORDS_STORE).clear();
      transaction.objectStore(CURRENT_STORE).clear();
      transaction.objectStore(CURRENT_META_STORE).clear();
      const metaKeys = await requestResult(legacyMetaStore.getAllKeys() as IDBRequest<IDBValidKey[]>);
      for (const key of metaKeys) {
        if (key !== CLEAR_EPOCH_KEY) legacyMetaStore.delete(key);
      }
      legacyMetaStore.put({ ref: CLEAR_EPOCH_KEY, epoch: observed + 1 });
      await complete;
      return { committed: true, epoch: observed + 1 };
    } catch {
      try {
        transaction?.abort();
      } catch {
        // The transaction may already have finished.
      }
      await complete?.catch(() => {});
      return { committed: false, epoch: observed };
    }
  }

  // Delete every entry row for one history identity within an open
  // transaction and return its accounted encoded bytes. Item rows go through
  // the historyKey index; turn and coverage rows through their composite key
  // range, so the identity leaves no partial rows behind.
  async #deleteHistoryRows(transaction: IDBTransaction, historyKey: string): Promise<number> {
    const itemStore = transaction.objectStore(ENTRY_ITEM_STORE);
    const turnStore = transaction.objectStore(ENTRY_TURN_STORE);
    const coverageStore = transaction.objectStore(ENTRY_COVERAGE_STORE);
    const metaStore = transaction.objectStore(ENTRY_META_STORE);
    const itemKeys = await requestResult(
      itemStore.index("historyKey").getAllKeys(historyKey) as IDBRequest<IDBValidKey[]>,
    );
    for (const key of itemKeys) itemStore.delete(key);
    const range = this.#keyRange.bound([historyKey, ""], [historyKey, "\uffff"]);
    const turnKeys = await requestResult(turnStore.getAllKeys(range) as IDBRequest<IDBValidKey[]>);
    for (const key of turnKeys) turnStore.delete(key);
    const coverageKeys = await requestResult(coverageStore.getAllKeys(range) as IDBRequest<IDBValidKey[]>);
    for (const key of coverageKeys) coverageStore.delete(key);
    const meta = await requestResult(metaStore.get(historyKey) as IDBRequest<TranscriptHistoryMetaRow | undefined>);
    // Preserve a non-zero fence across eviction and TTL: without the generation
    // row, later work captured before a delete would be re-admitted against the
    // now-empty identity. A generation-0 history has no fence to keep.
    if ((meta?.generation ?? 0) > 0) {
      metaStore.put({ historyKey, encodedBytes: 0, itemCount: 0, generation: meta?.generation });
    } else {
      metaStore.delete(historyKey);
    }
    return meta?.encodedBytes ?? 0;
  }

  // One coherent disk policy over every history population: entry histories,
  // legacy records and current checkpoints. TTL reclaims expired units of every
  // kind first, regardless of preference. Then eviction orders unpresferred
  // units before preferred ones and oldest first; the just-written identity is
  // never a victim and is refused instead if it cannot fit. Deleting a unit
  // removes its rows and coverage together inside this transaction; a reader's
  // in-memory pages are never touched, and a generation tombstone has no bytes
  // or timestamps so it can never be reclaimed.
  async #enforcePolicy(transaction: IDBTransaction, protect: RetentionUnit): Promise<EntryPolicyResult> {
    const metaStore = transaction.objectStore(ENTRY_META_STORE);
    const entryRows = (
      await requestResult(
        metaStore.getAll() as IDBRequest<
          Array<{ historyKey: string; encodedBytes?: number; savedAt?: number; accessedAt?: number }>
        >,
      )
    ).filter((row) => row.historyKey !== AGGREGATE_META_KEY);
    const legacyRows = (
      await requestResult(
        transaction.objectStore(LEGACY_META_STORE).getAll() as IDBRequest<
          Array<{ ref?: string; bytes?: number; savedAt?: number }>
        >,
      )
    ).filter((row) => row.ref !== CLEAR_EPOCH_KEY && typeof row.ref === "string");
    const currentRows = await requestResult(
      transaction.objectStore(CURRENT_META_STORE).getAll() as IDBRequest<
        Array<{ ownerToken?: string; ref?: string; bytes?: number; savedAt?: number }>
      >,
    );
    const now = this.#now();
    const stamp = (accessedAt: number | undefined, savedAt: number | undefined): number =>
      Number.isFinite(accessedAt) ? (accessedAt as number) : Number.isFinite(savedAt) ? (savedAt as number) : 0;
    const expired = (age: number): boolean => age > 0 && age + this.#ttlMs <= now;

    let entryTotal = entryRows.reduce((sum, row) => sum + (row.encodedBytes ?? 0), 0);
    let combined =
      entryTotal +
      legacyRows.reduce((sum, row) => sum + (row.bytes ?? 0), 0) +
      currentRows.reduce((sum, row) => sum + (row.bytes ?? 0), 0);

    for (const row of entryRows) {
      if ((row.encodedBytes ?? 0) === 0 || !expired(stamp(row.accessedAt, row.savedAt))) continue;
      const removed = await this.#deleteHistoryRows(transaction, row.historyKey);
      entryTotal -= removed;
      combined -= removed;
      row.encodedBytes = 0;
    }
    for (const row of legacyRows) {
      if ((row.bytes ?? 0) === 0 || typeof row.ref !== "string" || !expired(stamp(undefined, row.savedAt))) continue;
      combined -= await this.#deleteLegacyRecordRows(transaction, row.ref);
      row.bytes = 0;
    }
    for (const row of currentRows) {
      if (
        (row.bytes ?? 0) === 0 ||
        typeof row.ownerToken !== "string" ||
        typeof row.ref !== "string" ||
        !expired(stamp(undefined, row.savedAt))
      )
        continue;
      combined -= await this.#deleteCurrentRows(transaction, [row.ownerToken, row.ref]);
      row.bytes = 0;
    }

    if (combined > this.#maxBytes) {
      const victims: Array<{ unit: RetentionUnit; bytes: number; stamp: number; preferred: boolean }> = [];
      const add = (unit: RetentionUnit, bytes: number, age: number) => {
        victims.push({
          unit,
          bytes,
          stamp: age,
          preferred:
            this.#isPreferred?.(unit) ??
            (unit.kind === "entry" ? (this.#preferredHistories.get(unit.historyKey)?.() ?? false) : false),
        });
      };
      for (const row of entryRows) {
        if ((row.encodedBytes ?? 0) > 0) {
          add({ kind: "entry", historyKey: row.historyKey }, row.encodedBytes ?? 0, stamp(row.accessedAt, row.savedAt));
        }
      }
      for (const row of legacyRows) {
        if ((row.bytes ?? 0) > 0 && typeof row.ref === "string") {
          add({ kind: "records", ref: row.ref }, row.bytes ?? 0, stamp(undefined, row.savedAt));
        }
      }
      for (const row of currentRows) {
        if ((row.bytes ?? 0) > 0 && typeof row.ownerToken === "string" && typeof row.ref === "string") {
          add(
            { kind: "current", ownerToken: row.ownerToken, ref: row.ref },
            row.bytes ?? 0,
            stamp(undefined, row.savedAt),
          );
        }
      }
      const eligible = victims
        .filter((candidate) => !isProtectedUnit(candidate.unit, protect))
        .sort(
          (left, right) =>
            Number(left.preferred) - Number(right.preferred) ||
            left.stamp - right.stamp ||
            unitSortKey(left.unit).localeCompare(unitSortKey(right.unit)),
        );
      for (const victim of eligible) {
        if (combined <= this.#maxBytes) break;
        const removed =
          victim.unit.kind === "entry"
            ? await this.#deleteHistoryRows(transaction, victim.unit.historyKey)
            : victim.unit.kind === "records"
              ? await this.#deleteLegacyRecordRows(transaction, victim.unit.ref)
              : await this.#deleteCurrentRows(transaction, [victim.unit.ownerToken, victim.unit.ref]);
        combined -= removed;
        if (victim.unit.kind === "entry") {
          entryTotal -= removed;
          const row = entryRows.find(
            (candidate) => candidate.historyKey === (victim.unit as { historyKey: string }).historyKey,
          );
          if (row) row.encodedBytes = 0;
        }
      }
    }

    metaStore.put({ historyKey: AGGREGATE_META_KEY, encodedBytes: Math.max(0, entryTotal) });
    const protectRow =
      protect.kind === "entry" ? entryRows.find((row) => row.historyKey === protect.historyKey) : undefined;
    const protectedBytes = protectRow?.encodedBytes ?? 0;
    return {
      aggregateBytes: Math.max(0, entryTotal),
      protectedSurvived: protectRow !== undefined && protectedBytes > 0,
      overCapacity: protectRow !== undefined && protectedBytes > this.#maxBytes,
    };
  }

  // Delete one legacy record body and its meta accounting, returning the meta
  // bytes. The reserved epoch row is never a history row.
  async #deleteLegacyRecordRows(transaction: IDBTransaction, ref: string): Promise<number> {
    const meta = await requestResult(
      transaction.objectStore(LEGACY_META_STORE).get(ref) as IDBRequest<{ bytes?: number } | undefined>,
    );
    transaction.objectStore(LEGACY_RECORDS_STORE).delete(ref);
    transaction.objectStore(LEGACY_META_STORE).delete(ref);
    return Number.isFinite(meta?.bytes) ? (meta?.bytes as number) : 0;
  }

  // Delete one current checkpoint and its meta accounting, returning the meta
  // bytes.
  async #deleteCurrentRows(transaction: IDBTransaction, key: [string, string]): Promise<number> {
    const meta = await requestResult(
      transaction.objectStore(CURRENT_META_STORE).get(key) as IDBRequest<{ bytes?: number } | undefined>,
    );
    transaction.objectStore(CURRENT_STORE).delete(key);
    transaction.objectStore(CURRENT_META_STORE).delete(key);
    return Number.isFinite(meta?.bytes) ? (meta?.bytes as number) : 0;
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
        ...(input.requestCursor === undefined ? {} : { requestCursor: input.requestCursor }),
        ...(input.authority === undefined ? {} : { authority: input.authority }),
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
        current.serverCursor === row.serverCursor &&
        current.requestCursor === row.requestCursor &&
        current.authority === row.authority
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
   *
   * When `authority` is supplied, anchors and the connected component are
   * scoped to certificates carrying that opaque token, so a caller can never
   * anchor into another connection/read lifetime's chain. Scoping is defense
   * in depth: certificates of different authority never continue one another
   * even when no filter is passed.
   */
  async getOlderWindow(
    historyKey: string,
    before: ThreadItemPosition,
    limit: number,
    authority?: string,
  ): Promise<EntryWindow> {
    if (!isPosition(before)) throw new RangeError("entry older window requires a real boundary position");
    if (!Number.isInteger(limit) || limit < 0)
      throw new RangeError("entry window limit must be a non-negative integer");
    const database = await this.#connection.open();
    const transaction = database.transaction([ENTRY_ITEM_STORE, ENTRY_COVERAGE_STORE], "readonly");
    const complete = transactionCompletion(transaction);
    try {
      const coverageStore = transaction.objectStore(ENTRY_COVERAGE_STORE);
      const itemIndex = transaction.objectStore(ENTRY_ITEM_STORE).index("historyKey_position");
      const allRanges = await requestResult(
        coverageStore.getAll(this.#keyRange.bound([historyKey, ""], [historyKey, "\uffff"])) as IDBRequest<
          TranscriptCoverageRow[]
        >,
      );
      // Without an explicit authority a store holding several competing
      // connection/read lifetimes must not be certified: anchoring would pick
      // one authority's certificate by position and could present another
      // authority's live chain as exhausted. Only the caller that knows the
      // current lifetime may scope the continuation. An omitted authority is
      // itself a distinct value, so a mix of defined and undefined authorities
      // is also ambiguous.
      if (authority === undefined) {
        const authorities = new Set(allRanges.map((row) => row.authority));
        if (authorities.size > 1) {
          await complete;
          return { rows: [], coverage: [], complete: false, stale: true };
        }
      }
      const ranges = authority === undefined ? allRanges : allRanges.filter((row) => row.authority === authority);
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
      // The continuation belongs to the deepest unconsumed certificate,
      // resolved from recorded successor edges. A fully-exhausted chain that is
      // ambiguous is unverified, never presented as terminated history.
      const frontier = isComplete ? chooseFrontier(chain) : undefined;
      const verifiedComplete = isComplete && (frontier?.verified ?? false);
      return {
        rows,
        coverage: chain,
        complete: verifiedComplete,
        stale: isComplete && !(frontier?.verified ?? false),
        boundary: anchor.boundary,
        ...(verifiedComplete && frontier?.cursor !== undefined ? { serverCursor: frontier.cursor } : {}),
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
