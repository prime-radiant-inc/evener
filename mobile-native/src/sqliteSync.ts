// The one synchronous SQLite surface the native adapters share, named by the
// port rather than the host: expo-sqlite's openDatabaseSync in production,
// node:sqlite's DatabaseSync in tests. Both adapters (draftRepository's draft
// store, mutationOutboxStorage's outbox) and their test doubles implement this
// single shape, so one fake fits every suite and the savepoint helper below is
// written once instead of copied per adapter.
//
// Concurrency contract: a SqliteSync is exactly one connection, and this app
// opens exactly one per database file for the life of the process. The adapters
// therefore rely on SQLite's own single-connection serialization and install no
// busy handler: a second connection to the same file meets the engine's lock
// boundary ("database is locked") instead of waiting. Adopting concurrent
// handles is a deliberate change that must revisit this contract first.
export type SqliteSyncParam = string | number | null;

// The affected-row count expo-sqlite's runSync and node:sqlite's run() both
// report (sqlite3_changes64()), used to decide a write's boolean result from
// the statement itself instead of a preceding SELECT.
export interface SqliteSyncRunResult {
	changes: number | bigint;
}

export interface SqliteSync {
	execSync(sql: string): void;
	runSync(sql: string, ...params: SqliteSyncParam[]): SqliteSyncRunResult;
	getFirstSync<T>(sql: string, ...params: SqliteSyncParam[]): T | null;
	getAllSync<T>(sql: string, ...params: SqliteSyncParam[]): T[];
}

// Wraps a compound operation (a sequence allocation plus an insert, a
// multi-table settlement, an image/draft checkpoint) in one SQLite savepoint so
// a throw partway through rolls back every statement already run. Shared by
// draftRepository.write, creationDraftRepository's write/clear and
// MutationOutboxSQLite.transaction: one SAVEPOINT/ROLLBACK TO/RELEASE pattern
// instead of three copies that must stay in step.
export function withSavepoint<T>(db: SqliteSync, name: string, body: () => T): T {
	db.execSync(`SAVEPOINT ${name}`);
	try {
		const result = body();
		db.execSync(`RELEASE ${name}`);
		return result;
	} catch (error) {
		db.execSync(`ROLLBACK TO ${name}; RELEASE ${name}`);
		throw error;
	}
}
