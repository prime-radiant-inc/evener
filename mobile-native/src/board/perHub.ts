// Imports nothing, so a module that must not reach expo-sqlite/kv-store
// (hubSeen.ts, which the session screen imports) can keep its per-hub
// instances the same way nativeBoardMemory.ts does.

/** One instance per hub, made on first use and dropped when the hub is
 * forgotten. What the first get passes after the hub goes to the maker, so an
 * instance keeps what it was made with. */
export function perHub<T, A extends unknown[] = []>(make: (hubId: string, ...args: A) => T) {
	// Each instance sits in its own entry, so one that is falsy is still made.
	const instances = new Map<string, { instance: T }>();
	return {
		/** Reads an existing instance without creating one. */
		peek(hubId: string): T | undefined {
			return instances.get(hubId)?.instance;
		},
		get(hubId: string, ...args: A): T {
			let entry = instances.get(hubId);
			if (!entry) {
				entry = { instance: make(hubId, ...args) };
				instances.set(hubId, entry);
			}
			return entry.instance;
		},
		/** Drops the hub's instance, returning it so its owner can close it. */
		forget(hubId: string): T | undefined {
			const entry = instances.get(hubId);
			instances.delete(hubId);
			return entry?.instance;
		},
	};
}
