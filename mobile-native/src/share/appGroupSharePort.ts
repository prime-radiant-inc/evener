// The app group the share extension writes into, and the app reads back. The
// identifier is the app's bundle id with the group. prefix, the same default
// @bacons/apple-targets applies to the extension's entitlements, so the two
// sides can't drift apart in configuration without this changing too.
import { File, Paths } from "expo-file-system";
import { isShareFileName, type SharePort } from "./sharePayload";

export const SHARE_APP_GROUP = "group.com.primeradiant.evener.native";

/** One file in the share directory. */
export interface ShareGroupEntry {
	name: string;
	text(): Promise<string>;
	delete(): void;
}

/** The slice of expo-file-system's Directory this port needs, so tests can
 * hand in a directory of their own. */
export interface ShareGroupDirectory {
	exists: boolean;
	list(): ShareGroupEntry[];
	/** The entry named `name`, or null when the directory has no such file.
	 * Reads and deletes go through it, so neither re-enumerates the
	 * directory. */
	file(name: string): ShareGroupEntry | null;
}

/** expo-file-system's Directory, narrowed to what this port reads. A file is
 * resolved by path (`new File`), never by listing: one share pass lists the
 * directory once, however many files it holds. */
function entriesOf(directory: {
	exists: boolean;
	list(): Array<{ name: string; text(): Promise<string>; delete(): void }>;
	uri: string;
}): ShareGroupDirectory {
	return {
		exists: directory.exists,
		list: () => directory.list(),
		file(name) {
			const file = new File(directory.uri, name);
			return file.exists ? file : null;
		},
	};
}

/** The App Group container, or null when the entitlement is missing (a build
 * without the extension's app group). */
export function sharedContainer(): ShareGroupDirectory | null {
	try {
		const directory = Paths.appleSharedContainers[SHARE_APP_GROUP];
		return directory ? entriesOf(directory as never) : null;
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
		return group.file(name);
	};
	return {
		async list() {
			const group = directory();
			if (!group || !group.exists) return [];
			return group
				.list()
				.map((entry) => entry.name)
				.filter(isShareFileName);
		},
		async read(name) {
			const entry = find(name);
			if (!entry) throw new Error(`Share ${name} is no longer there.`);
			return entry.text();
		},
		async remove(name) {
			find(name)?.delete();
		},
	};
}
