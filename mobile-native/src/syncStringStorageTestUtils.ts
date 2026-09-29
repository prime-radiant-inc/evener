// An in-memory SyncStringStorage for tests: the sync key-value shape the app
// keeps in expo-sqlite/kv-store, backed by a Map a test can seed and inspect.
// Suites used to redefine this helper inline, byte for byte (#2687); this is
// the one copy they import.
import type { SyncStringStorage } from "./syncStringStorage";

export function memoryStorage(values = new Map<string, string>()): SyncStringStorage & { values: Map<string, string> } {
	return {
		values,
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}
