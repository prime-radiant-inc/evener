export const ENTRY_ITEM_STORE = "transcriptItems";
export const ENTRY_TURN_STORE = "transcriptTurns";
export const ENTRY_COVERAGE_STORE = "transcriptCoverage";
export const ENTRY_META_STORE = "transcriptMeta";
export const LEGACY_RECORDS_STORE = "records";
export const LEGACY_META_STORE = "meta";
export const CURRENT_STORE = "current";
export const CURRENT_META_STORE = "currentMeta";
const CLEAR_EPOCH_KEY = "__clearEpoch";

export interface TranscriptItemRow<T = unknown> {
  historyKey: string;
  transcriptKey: string;
  position: { entry: number; item: number; sub?: number };
  version: number;
  item: T;
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
    items.createIndex("historyKey_position", ["historyKey", "position.entry", "position.item"], { unique: false });
    items.createIndex("historyKey", "historyKey", { unique: false });
  }
  if (!database.objectStoreNames.contains(ENTRY_TURN_STORE)) {
    database.createObjectStore(ENTRY_TURN_STORE, { keyPath: ["historyKey", "turnId"] });
  }
  if (!database.objectStoreNames.contains(ENTRY_COVERAGE_STORE)) {
    database.createObjectStore(ENTRY_COVERAGE_STORE, { keyPath: ["historyKey", "rangeId"] });
  }
  if (!database.objectStoreNames.contains(ENTRY_META_STORE)) {
    database.createObjectStore(ENTRY_META_STORE, { keyPath: "historyKey" });
  }
}
