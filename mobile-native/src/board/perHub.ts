// Imports nothing, so a module that must not reach expo-sqlite/kv-store
// (hubSeen.ts, which the session screen imports) can keep its per-hub
// instances the same way nativeBoardMemory.ts does.

/** One instance per hub, made on first use and dropped when the hub is forgotten. */
export function perHub<T>(make: (hubId: string) => T) {
	const instances = new Map<string, T>();
	return {
		get(hubId: string): T {
			let instance = instances.get(hubId);
			if (!instance) {
				instance = make(hubId);
				instances.set(hubId, instance);
			}
			return instance;
		},
		forget(hubId: string): void {
			instances.delete(hubId);
		},
	};
}
