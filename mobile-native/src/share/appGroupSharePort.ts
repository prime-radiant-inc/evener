// The app group the share extension writes into, and the app reads back. The
// identifier is the app's bundle id with the group. prefix, the same default
// @bacons/apple-targets applies to the extension's entitlements, so the two
// sides can't drift apart in configuration without this changing too.
import { Paths } from "expo-file-system";
import type { SharePort } from "./sharePayload";

export const SHARE_APP_GROUP = "group.com.primeradiant.evener.native";

/** The slice of expo-file-system's Directory this port needs, so tests can
 * hand in a directory of their own. */
export interface ShareGroupDirectory {
	exists: boolean;
	list(): Array<{ name: string; text(): Promise<string>; delete(): void }>;
}

/** The App Group container, or null when the entitlement is missing (a build
 * without the extension's app group). */
export function sharedContainer(): ShareGroupDirectory | null {
	try {
		return (Paths.appleSharedContainers[SHARE_APP_GROUP] as unknown as ShareGroupDirectory) ?? null;
	} catch {
		return null;
	}
}

/** The share directory as the app's intake reads it. `directory` is resolved on
 * every call, so a container that appears after launch is picked up. */
export function appGroupSharePort(directory: () => ShareGroupDirectory | null = sharedContainer): SharePort {
	const find = (name: string) => {
		const group = directory();
		if (!group || !group.exists) return null;
		return group.list().find((entry) => entry.name === name) ?? null;
	};
	return {
		async list() {
			const group = directory();
			if (!group || !group.exists) return [];
			return group
				.list()
				.map((entry) => entry.name)
				.filter((name) => name.startsWith("share-") && name.endsWith(".json"));
		},
		async read(name) {
			const entry = find(name);
			if (!entry) throw new Error(`Share ${name} is no longer there.`);
			return entry.text();
		},
		async remove(name) {
			find(name)?.delete();
		},
		now: () => Date.now(),
	};
}
