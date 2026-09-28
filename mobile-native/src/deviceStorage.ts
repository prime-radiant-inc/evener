// Small JSON records this device keeps in expo-sqlite's kv-store: the Board's
// seen markers and folded sections, a document's reading state, the stop
// requests you sent. Every read and write is guarded, because the store can
// throw, and a record that doesn't parse reads as absent.
import type { SyncStringStorage } from "./syncStringStorage";

export function readJson(storage: SyncStringStorage, key: string): unknown {
	try {
		const raw = storage.getItemSync(key);
		return raw ? JSON.parse(raw) : null;
	} catch {
		return null;
	}
}

export function writeJson(storage: SyncStringStorage, key: string, value: unknown): void {
	try {
		storage.setItemSync(key, JSON.stringify(value));
	} catch {
		// The in-memory copy still serves this launch.
	}
}

export function removeKeys(storage: SyncStringStorage, keys: readonly string[]): void {
	let failed = false;
	for (const key of keys)
		try {
			storage.removeItemSync(key);
		} catch {
			// Keep trying the other keys, the way forgetBoard does
			// (board/boardMemory.ts): a storage failure orphans this one, but the
			// caller must still hear about it, so it's reported once every key has
			// been tried.
			failed = true;
		}
	if (failed) throw new Error("removeKeys: could not remove one or more keys from storage");
}
