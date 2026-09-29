// Imports nothing, so a module that must not reach expo-sqlite/kv-store
// (hubSeen.ts, which the session screen imports) can keep its per-hub
// instances the same way nativeBoardMemory.ts does.

/** One instance per hub, made on first use and dropped when the hub is
 * forgotten. What the first get passes after the hub goes to the maker, so an
 * instance keeps what it was made with. */
export function perHub<T, A extends unknown[] = []>(make: (hubId: string, ...args: A) => T) {
	const instances = new Map<string, T>();
	return {
		get(hubId: string, ...args: A): T {
			if (!instances.has(hubId)) instances.set(hubId, make(hubId, ...args));
			return instances.get(hubId) as T;
		},
		/** Drops the hub's instance, returning it so its owner can close it. */
		forget(hubId: string): T | undefined {
			const instance = instances.get(hubId);
			instances.delete(hubId);
			return instance;
		},
	};
}
