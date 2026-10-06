// Proves the shared SqliteSync port, its one savepoint helper, and the single
// node:sqlite double every sqlite-backed suite now builds from. The rollback
// contract here is the one draftRepository.write, creationDraftRepository's
// write/clear and MutationOutboxSQLite.transaction all rely on, so it is
// asserted once against the shared implementation instead of per adapter.
import { afterEach, expect, test } from "vitest";
import { type SqliteSync, withSavepoint } from "./sqliteSync";
import { openSqliteSyncDouble, sqliteSyncDouble, withoutSqliteExperimentalWarning } from "./sqliteSync.testkit";

const close: (() => void)[] = [];
afterEach(() => {
	for (const closeDatabase of close.splice(0)) closeDatabase();
});

function fixture() {
	const opened = openSqliteSyncDouble();
	close.push(() => opened.database.close());
	return opened;
}

test("the savepoint helper rolls back every statement a failed body ran", () => {
	const { port } = fixture();
	port.execSync("CREATE TABLE t (id INTEGER PRIMARY KEY, label TEXT NOT NULL)");
	port.execSync("INSERT INTO t (label) VALUES ('before')");
	expect(() =>
		withSavepoint(port, "shared_savepoint", () => {
			port.runSync("INSERT INTO t (label) VALUES (?)", "inside");
			throw new Error("boom");
		}),
	).toThrow("boom");
	expect(port.getAllSync<{ label: string }>("SELECT label FROM t ORDER BY id").map((row) => row.label)).toEqual([
		"before",
	]);
});

test("the savepoint helper commits a body that returns its result", () => {
	const { port } = fixture();
	port.execSync("CREATE TABLE t (label TEXT NOT NULL)");
	expect(
		withSavepoint(port, "shared_savepoint", () => {
			port.runSync("INSERT INTO t (label) VALUES (?)", "kept");
			return 7;
		}),
	).toBe(7);
	expect(port.getAllSync<{ label: string }>("SELECT label FROM t").map((row) => row.label)).toEqual(["kept"]);
});

test("the shared double maps every SqliteSync method onto node:sqlite", () => {
	const { database, port } = fixture();
	port.execSync("CREATE TABLE t (id INTEGER PRIMARY KEY, label TEXT)");
	expect(Number(port.runSync("INSERT INTO t (label) VALUES (?)", "one").changes)).toBe(1);
	expect(port.getFirstSync<{ label: string }>("SELECT label FROM t WHERE id = ?", 1)).toEqual({ label: "one" });
	expect(port.getFirstSync("SELECT label FROM t WHERE id = ?", 999)).toBeNull();
	expect(port.getAllSync<{ label: string }>("SELECT label FROM t")).toEqual([{ label: "one" }]);
	// The same handle the double wraps stays available for raw row assertions.
	expect(database.prepare("SELECT COUNT(*) AS count FROM t").get()).toEqual({ count: 1 });
});

test("wrapping an existing handle and a spread override share the one double", () => {
	const { database, port } = fixture();
	port.execSync("CREATE TABLE t (label TEXT NOT NULL)");
	const overridden: SqliteSync = {
		...sqliteSyncDouble(database),
		runSync: () => {
			throw new Error("refused");
		},
	};
	expect(() => overridden.runSync("INSERT INTO t (label) VALUES (?)", "nope")).toThrow("refused");
	expect(port.getAllSync("SELECT * FROM t")).toEqual([]);
});

// Node 22 warns once per process that node:sqlite is experimental. The double
// owns that one expected warning: it drops exactly that line while it loads the
// module, and lets every other warning through, so the gate's output stays
// clean without hiding warnings nobody expected (#3672).
test("drops only node:sqlite's experimental warning while the double loads it", () => {
	const emit = process.emitWarning;
	const seen: string[] = [];
	process.emitWarning = ((warning: string | Error) => {
		seen.push(typeof warning === "string" ? warning : warning.message);
	}) as typeof process.emitWarning;
	try {
		const loaded = withoutSqliteExperimentalWarning(() => {
			process.emitWarning("SQLite is an experimental feature and might change at any time", "ExperimentalWarning");
			process.emitWarning("Some other feature is experimental", "ExperimentalWarning");
			process.emitWarning(new Error("an unrelated warning"));
			return "module";
		});
		expect(loaded).toBe("module");
		expect(seen).toEqual(["Some other feature is experimental", "an unrelated warning"]);
		// Once the module has loaded, the filter is gone.
		process.emitWarning("SQLite is an experimental feature and might change at any time", "ExperimentalWarning");
		expect(seen).toHaveLength(3);
	} finally {
		process.emitWarning = emit;
	}
});

test("puts the warning hook back even when loading the module throws", () => {
	const emit = process.emitWarning;
	expect(() =>
		withoutSqliteExperimentalWarning(() => {
			throw new Error("no sqlite here");
		}),
	).toThrow("no sqlite here");
	expect(process.emitWarning).toBe(emit);
});
