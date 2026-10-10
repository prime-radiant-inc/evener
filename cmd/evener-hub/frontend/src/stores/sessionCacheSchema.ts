import type { ThreadItemPosition, TurnModel } from "@evener/appwire-client";

export const SESSION_CACHE_DATABASE_NAME = "evener-session-cache";
export const SESSION_CACHE_DATABASE_VERSION = 3;
export const ENTRY_ITEM_STORE = "transcriptItems";
export const ENTRY_TURN_STORE = "transcriptTurns";
export const ENTRY_COVERAGE_STORE = "transcriptCoverage";
export const ENTRY_META_STORE = "transcriptMeta";
export const ENTRY_COVERAGE_BOUNDARY_SNAPSHOT_INDEX = "historyKey_boundary_snapshot";
export const LEGACY_RECORDS_STORE = "records";
export const LEGACY_META_STORE = "meta";
export const CURRENT_STORE = "current";
export const CURRENT_META_STORE = "currentMeta";
const CLEAR_EPOCH_KEY = "__clearEpoch";

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

export interface TranscriptSessionMetadata {
  historyKey: string;
  requestedRefs: readonly TranscriptRequestedRef[];
  resolvedIdentity: TranscriptResolvedIdentity;
  savedAt: number;
  accessedAt: number;
  formatVersion: number;
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
  // Durable per-history fence generation. Deletion advances it so work captured
  // before the delete cannot re-enter the identity; absent means 0.
  generation?: number;
}

export interface TranscriptAggregateMetaRow {
  historyKey: "__aggregate";
  encodedBytes: number;
}

export interface EntryClearEpochRow {
  ref: typeof CLEAR_EPOCH_KEY;
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

export function upgradeSessionCache(database: IDBDatabase, includeEntryStores = false): void {
  if (!database.objectStoreNames.contains(LEGACY_RECORDS_STORE)) {
    database.createObjectStore(LEGACY_RECORDS_STORE, { keyPath: "ref" });
  }
  if (!database.objectStoreNames.contains(LEGACY_META_STORE)) {
    const meta = database.createObjectStore(LEGACY_META_STORE, { keyPath: "ref" });
    meta.put({ ref: CLEAR_EPOCH_KEY, epoch: 0 });
  }
  if (!database.objectStoreNames.contains(CURRENT_STORE)) {
    const current = database.createObjectStore(CURRENT_STORE, { keyPath: ["ownerToken", "ref"] });
    current.createIndex("ref", "ref", { unique: false });
  }
  if (!database.objectStoreNames.contains(CURRENT_META_STORE)) {
    const currentMeta = database.createObjectStore(CURRENT_META_STORE, { keyPath: ["ownerToken", "ref"] });
    currentMeta.createIndex("ref", "ref", { unique: false });
  }
  if (!includeEntryStores) return;
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
