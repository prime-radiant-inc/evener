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
		try {
			db.execSync(`ROLLBACK TO ${name}; RELEASE ${name}`);
		} catch {
			// A full disk or an I/O error makes SQLite end the transaction itself,
			// so the savepoint is already gone ("no such savepoint"). The failure
			// worth reporting is the original one.
		}
		throw error;
	}
}

// The messages SQLite gives for the result codes where the storage itself
// failed, not what was asked of it. expo-sqlite surfaces only these messages
// (as "Error code N: <message>" or the bare message, with no structured code),
// and node:sqlite carries the same text, so the text is the one handle both
// share. A constraint violation, a missing table or a syntax error is a bug in
// the adapter and is absent on purpose.
const STORAGE_FAILURE_MESSAGES: readonly string[] = [
	"database or disk is full", // SQLITE_FULL
	"disk i/o error", // SQLITE_IOERR
	"unable to open database file", // SQLITE_CANTOPEN
	"attempt to write a readonly database", // SQLITE_READONLY
	"database is locked", // SQLITE_BUSY
	"database disk image is malformed", // SQLITE_CORRUPT
	"file is not a database", // SQLITE_NOTADB
	"out of memory", // SQLITE_NOMEM
];

/** Whether a SQLite call failed because storage did (a full disk, an I/O
 * error, a database that cannot be opened or written), as opposed to a bug in
 * the SQL or the record. Only these earn the send's direct-request fallback. */
export function isSqliteStorageFailure(error: unknown): boolean {
	const message = error instanceof Error ? error.message.toLowerCase() : "";
	return STORAGE_FAILURE_MESSAGES.some((known) => message.includes(known));
}
