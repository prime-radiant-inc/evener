// The native expo-sqlite adapter's half of the package's MutationOutboxStorage
// port-conformance suite: the native factory hosts the shared contracts over
// node:sqlite's real engine, the same contracts the web IndexedDB adapter runs
// (cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.conformance.test.ts).
// One set of contracts, two hosts.
import { describeMutationOutboxStorage } from "@evener/appwire-client/testing/mutationOutboxStorageConformance";
import { afterEach, vi } from "vitest";
import { MutationOutboxSQLite, TABLES } from "./mutationOutboxStorage";
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

// Each conformance instance's raw database, so the legacy contracts can
// rewrite a record exactly as the pre-unresolvedAttempts schema left it.
const databaseFor = new WeakMap<MutationOutboxSQLite, SqliteDoubleDatabase>();

describeMutationOutboxStorage({
	name: "expo-sqlite",
	createStorage(options = {}) {
		const { database, port } = openSqliteSyncDouble();
		databases.push(database);
		const storage = new MutationOutboxSQLite(port, {
			createMutationId: options.createMutationId,
			now: options.now,
			getOwnClientId: options.getOwnClientId,
		});
		databaseFor.set(storage, database);
		return storage;
	},
	stripAttemptCount(_storage, clientMutationId) {
		const database = databaseFor.get(_storage as MutationOutboxSQLite);
		if (!database) throw new Error("conformance storage missing its raw database");
		// The pre-field column value: attempted evidence with a zero count,
		// exactly as the pre-unresolvedAttempts schema left it.
		database
			.prepare(`UPDATE ${TABLES.outbox} SET unresolved_attempts = 0 WHERE client_mutation_id = ?`)
			.run(clientMutationId);
	},
	stripCaptureAtEnqueue(_storage, clientMutationId) {
		const database = databaseFor.get(_storage as MutationOutboxSQLite);
		if (!database) throw new Error("conformance storage missing its raw database");
		// The pre-capture column value: the migration's DEFAULT 0, exactly
		// as a pre-field row reads after the upgrade.
		database
			.prepare(`UPDATE ${TABLES.outbox} SET stop_epoch_at_enqueue = 0 WHERE client_mutation_id = ?`)
			.run(clientMutationId);
	},
});
