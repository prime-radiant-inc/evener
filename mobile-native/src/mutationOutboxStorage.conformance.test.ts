// The native expo-sqlite adapter's half of the package's MutationOutboxStorage
// port-conformance suite: the native factory hosts the shared contracts over
// node:sqlite's real engine, the same contracts the web IndexedDB adapter runs
// (cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.conformance.test.ts).
// One set of contracts, two hosts.
import { describeMutationOutboxStorage } from "@evener/appwire-client/testing/mutationOutboxStorageConformance";
import { afterEach, vi } from "vitest";
import { MutationOutboxSQLite } from "./mutationOutboxStorage";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

// The adapter's default id source is expo-crypto, which is unavailable outside
// a device or simulator; the injected createMutationId above is what the
// contracts use, but the constructor still resolves the default source.
vi.mock("expo-crypto", () => ({
	randomUUID: () => "expo-crypto-mock",
	getRandomValues: (array: Uint8Array) => array,
}));

const databases: SqliteDoubleDatabase[] = [];
afterEach(() => {
	for (const database of databases.splice(0)) database.close();
});

describeMutationOutboxStorage({
	name: "expo-sqlite",
	createStorage(options = {}) {
		const { database, port } = openSqliteSyncDouble();
		databases.push(database);
		return new MutationOutboxSQLite(port, {
			createMutationId: options.createMutationId,
			now: options.now,
			getOwnClientId: options.getOwnClientId,
		});
	},
});
