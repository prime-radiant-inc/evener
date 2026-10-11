/** The sync string key-value shape the app keeps in expo-sqlite/kv-store.
 * In-memory fakes implement this same interface in tests. */
export interface SyncStringStorage {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
	removeItemSync(key: string): void;
}
