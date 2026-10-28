import type { CachedSessionRecord, ThreadItemPosition, TurnModel } from "@evener/appwire-client";

export const SESSION_CACHE_DATABASE_NAME = "evener-session-cache";
// Bumped to 4 with the 6d legacy-store removal: the entry cache is the only
// route, so the four legacy stores (records/meta/current/currentMeta) are
// dropped on upgrade and the durable Clear epoch moves into ENTRY_META_STORE.
export const SESSION_CACHE_DATABASE_VERSION = 4;
export const ENTRY_ITEM_STORE = "transcriptItems";
export const ENTRY_TURN_STORE = "transcriptTurns";
export const ENTRY_COVERAGE_STORE = "transcriptCoverage";
export const ENTRY_META_STORE = "transcriptMeta";
export const ENTRY_COVERAGE_BOUNDARY_SNAPSHOT_INDEX = "historyKey_boundary_snapshot";
// The reserved key of the single durable Clear epoch row, which is entry-owned:
// it lives in ENTRY_META_STORE keyed by this historyKey. It is not a history row
// and every enumeration skips it by key comparison.
export const CLEAR_EPOCH_KEY = "__clearEpoch";
// The repository's own short deadline for bounded internal reads: paging
// continuation decisions, the cached shell install, and durable authority
// reads, where a slow answer falls back to the server or fails closed. The
// pane's foreground open no longer uses it - that open races the source's
// settlement with a long storage backstop instead.
export const SESSION_CACHE_LOOKUP_DEADLINE_MS = 250;
// The legacy stores dropped at the 6d schema bump. Named here only so the
// upgrade can discard them.
const LEGACY_STORE_NAMES = ["records", "meta", "current", "currentMeta"] as const;

export interface TranscriptItemRow<T = unknown> {
  historyKey: string;
  transcriptKey: string;
  position: ThreadItemPosition;
  version: number;
  item: T;
}

export interface TranscriptTurnRow {
  historyKey: string;
  turnId: string;
  version?: number;
  metadata: Omit<TurnModel, "id" | "items">;
}

export interface TranscriptCoverageRow {
  historyKey: string;
  rangeId: string;
  kind: "read" | "suffix" | "page";
  start: ThreadItemPosition | null;
  end: ThreadItemPosition | null;
  representedSnapshot: number;
  serverCursor?: string;
  readId?: string;
  continuityId?: string;
  boundary: string;
  suffixBlocked?: boolean;
  // Recorded only for a page certificate: the exclusive request boundary the
  // page was fetched against, the provenance link between consecutive pages.
  // Never parsed as an opaque cursor.
  before?: ThreadItemPosition | null;
  // Recorded only for a page certificate: the opaque request cursor the page
  // consumed (`ThreadTurnsListParams.cursor`), undefined for a position-only
  // request. Together with `serverCursor` it forms the successor edge that
  // establishes continuation authority; never parsed.
  requestCursor?: string;
  // Opaque equality token for the source authority that owns this certificate's
  // chain (the repository binding's tab-local connection/read lifetime). Two
  // certificates may only continue one another when their tokens are equal;
  // never parsed, and undefined when the writer supplied none.
  authority?: string;
}

export interface TranscriptRequestedRef {
  clientId: string;
  ref: string;
}

export interface TranscriptResolvedIdentity {
  clientId: string;
  hostId: string;
  sessionId: string;
  incarnation: string;
}

// Source-derived display scalars only. Recorded bodies remain keyed rows, and
// this header grants neither live capabilities nor a current paging baseline.
export type TranscriptCachedHeader = Omit<CachedSessionRecord, "history"> & {
  history: Omit<CachedSessionRecord["history"], "turns">;
};

export interface TranscriptSessionMetadata {
  historyKey: string;
  requestedRefs: readonly TranscriptRequestedRef[];
  resolvedIdentity: TranscriptResolvedIdentity;
  savedAt: number;
  accessedAt: number;
  formatVersion: number;
  cachedHeader?: TranscriptCachedHeader;
}

export interface TranscriptHistoryMetaRow {
  historyKey: string;
  encodedBytes: number;
  itemCount: number;
  requestedRefs?: readonly TranscriptRequestedRef[];
  resolvedIdentity?: TranscriptResolvedIdentity;
  savedAt?: number;
  accessedAt?: number;
  formatVersion?: number;
  cachedHeader?: TranscriptCachedHeader;
  // Durable per-history fence generation. Deletion advances it so work captured
  // before the delete cannot re-enter the identity; absent means 0.
  generation?: number;
}

export interface TranscriptAggregateMetaRow {
  historyKey: "__aggregate";
  encodedBytes: number;
}

// The durable Clear epoch row. Entry-owned: it lives in ENTRY_META_STORE keyed
// by CLEAR_EPOCH_KEY.
export interface EntryClearEpochRow {
  historyKey: typeof CLEAR_EPOCH_KEY;
  epoch: number;
}

export function entryHistoryKey(identity: {
  clientId: string;
  hostId: string;
  sessionId: string;
  incarnation: string;
}): string {
  return JSON.stringify([identity.clientId, identity.hostId, identity.sessionId, identity.incarnation]);
}

export function upgradeSessionCache(database: IDBDatabase): void {
  // The legacy stores are discarded on upgrade (Ruling 37): the cache is
  // rebuilt from the server, never migrated. IndexedDB keeps any store not
  // explicitly deleted, so remove them here.
  for (const name of LEGACY_STORE_NAMES) {
    if (database.objectStoreNames.contains(name)) database.deleteObjectStore(name);
  }
  if (!database.objectStoreNames.contains(ENTRY_ITEM_STORE)) {
    const items = database.createObjectStore(ENTRY_ITEM_STORE, { keyPath: ["historyKey", "transcriptKey"] });
    items.createIndex("historyKey_position", ["historyKey", "position.entry", "position.item", "positionSub"], {
      unique: false,
    });
    items.createIndex("historyKey", "historyKey", { unique: false });
  }
  if (!database.objectStoreNames.contains(ENTRY_TURN_STORE)) {
    database.createObjectStore(ENTRY_TURN_STORE, { keyPath: ["historyKey", "turnId"] });
  }
  if (!database.objectStoreNames.contains(ENTRY_COVERAGE_STORE)) {
    const coverage = database.createObjectStore(ENTRY_COVERAGE_STORE, { keyPath: ["historyKey", "rangeId"] });
    coverage.createIndex("historyKey_boundary", ["historyKey", "boundary"], { unique: false });
    coverage.createIndex(ENTRY_COVERAGE_BOUNDARY_SNAPSHOT_INDEX, ["historyKey", "boundary", "representedSnapshot"], {
      unique: false,
    });
  }
  if (!database.objectStoreNames.contains(ENTRY_META_STORE)) {
    database.createObjectStore(ENTRY_META_STORE, { keyPath: "historyKey" });
  }
}
