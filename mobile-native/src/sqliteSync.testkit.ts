// The one node:sqlite test double for every sqlite-backed native suite. It maps
// node:sqlite's DatabaseSync onto the shared SqliteSync port (sqliteSync.ts) so
// a suite never hand-rolls the adapter again, and the raw handle stays available
// for the persistence assertions that inspect rows where the stored encoding is
// the contract - expo-sqlite is unavailable outside a device or simulator.
import type { DatabaseSync } from "node:sqlite";
import type { SqliteSync, SqliteSyncParam } from "./sqliteSync";

const SQLITE_EXPERIMENTAL = "SQLite is an experimental feature";

/** Runs `load` with node:sqlite's experimental warning (Node 22 emits it once
 * per process, the first time the module loads) dropped, and every other
 * warning passed through: the gate's output stays clean without hiding a
 * warning nobody expected (#3672). */
export function withoutSqliteExperimentalWarning<T>(load: () => T): T {
	const emit = process.emitWarning;
	process.emitWarning = function (this: unknown, warning: string | Error, ...rest: unknown[]) {
		const message = typeof warning === "string" ? warning : warning.message;
		if (message.startsWith(SQLITE_EXPERIMENTAL)) return;
		return (emit as (...args: unknown[]) => void).call(this, warning, ...rest);
	} as typeof process.emitWarning;
	try {
		return load();
	} finally {
		process.emitWarning = emit;
	}
}

// Loaded through the filter rather than a static import, which would load the
// module (and emit its warning) before any code here runs.
const { DatabaseSync: Database } = withoutSqliteExperimentalWarning(() => process.getBuiltinModule("node:sqlite"));

/** The node:sqlite handle the double wraps, for suites that assert raw rows. */
export type SqliteDoubleDatabase = DatabaseSync;

/** Wraps an existing node:sqlite handle as the shared SqliteSync port. */
export function sqliteSyncDouble(database: DatabaseSync): SqliteSync {
	return {
		execSync: (sql) => database.exec(sql),
		runSync: (sql, ...params) => database.prepare(sql).run(...params),
		getFirstSync: <T>(sql: string, ...params: SqliteSyncParam[]) =>
			(database.prepare(sql).get(...params) as T | undefined) ?? null,
		getAllSync: <T>(sql: string, ...params: SqliteSyncParam[]) => database.prepare(sql).all(...params) as T[],
	};
}

/** Opens a fresh node:sqlite database and returns it with its port adapter. */
export function openSqliteSyncDouble(location = ":memory:"): {
	database: DatabaseSync;
	port: SqliteSync;
} {
	const database = new Database(location);
	return { database, port: sqliteSyncDouble(database) };
}
