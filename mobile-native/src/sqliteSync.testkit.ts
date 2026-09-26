// The one node:sqlite test double for every sqlite-backed native suite. It maps
// node:sqlite's DatabaseSync onto the shared SqliteSync port (sqliteSync.ts) so
// a suite never hand-rolls the adapter again, and the raw handle stays available
// for the persistence assertions that inspect rows where the stored encoding is
// the contract - expo-sqlite is unavailable outside a device or simulator.
import { DatabaseSync } from "node:sqlite";
import type { SqliteSync, SqliteSyncParam } from "./sqliteSync";

/** The node:sqlite handle the double wraps, for suites that assert raw rows. */
export type SqliteDoubleDatabase = DatabaseSync;

/** Wraps an existing node:sqlite handle as the shared SqliteSync port. */
export function sqliteSyncDouble(database: DatabaseSync): SqliteSync {
	return {
		execSync: (sql) => database.exec(sql),
		runSync: (sql, ...params) => database.prepare(sql).run(...params),
		getFirstSync: <T>(sql: string, ...params: SqliteSyncParam[]) =>
			(database.prepare(sql).get(...params) as T | undefined) ?? null,
		getAllSync: <T>(sql: string, ...params: SqliteSyncParam[]) =>
			database.prepare(sql).all(...params) as T[],
	};
}

/** Opens a fresh node:sqlite database and returns it with its port adapter. */
export function openSqliteSyncDouble(location = ":memory:"): {
	database: DatabaseSync;
	port: SqliteSync;
} {
	const database = new DatabaseSync(location);
	return { database, port: sqliteSyncDouble(database) };
}
