// The context a New session page reads, as NewSessionSheet builds it, for
// tests that mount one page: the hub is "magic-kingdom" (hub-1), its own
// machine is named after it, and the launch memory lives in memory.
import type { SyncStringStorage } from "../syncStringStorage";
import { LaunchMemory } from "./launchMemory";
import type { NewSessionContextValue, NewSessionStore } from "./newSessionContext";

export function memoryStorage(): SyncStringStorage {
	const items = new Map<string, string>();
	return {
		getItemSync: (key) => items.get(key) ?? null,
		setItemSync: (key, value) => void items.set(key, value),
		removeItemSync: (key) => void items.delete(key),
	};
}

export function sheetContext(
	store: NewSessionStore,
	over: Partial<NewSessionContextValue> = {},
): NewSessionContextValue {
	return {
		store,
		hubId: "hub-1",
		hubName: "magic-kingdom",
		client: null,
		ready: true,
		hosts: null,
		live: null,
		memory: new LaunchMemory(memoryStorage(), "hub-1"),
		hostLabel: (host) => (host === "local" ? "magic-kingdom" : host),
		...over,
	};
}
