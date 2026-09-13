import { DatabaseSync } from "node:sqlite";
import { afterEach, describe, expect, it } from "vitest";
import { DraftLibrary } from "./draftLibrary";
import { DraftRepository } from "./draftRepository";

const databases: DatabaseSync[] = [];
afterEach(() => {
	for (const db of databases.splice(0)) db.close();
});
function setup() {
	const db = new DatabaseSync(":memory:");
	databases.push(db);
	const repository = new DraftRepository({
		execSync: (sql) => db.exec(sql),
		runSync: (sql, ...params) => db.prepare(sql).run(...params),
		getFirstSync: <T>(sql: string, ...params: string[]) =>
			(db.prepare(sql).get(...params) as T | undefined) ?? null,
	});
	return { library: new DraftLibrary(() => repository), repository, db };
}

describe("drafts across navigation", () => {
	it("invalidates open documents even if deleting persisted data fails", () => {
		const { library, repository, db } = setup();
		const destination = { hubId: "one", sessionRef: "session" };
		const document = library.open(destination);
		document.edit("saved");
		db.exec("PRAGMA query_only = ON");
		expect(() => library.removeHub("one")).toThrow();
		document.edit("must not write");
		expect(repository.read(destination).draft).toBe("saved");
		expect(document.getSnapshot().loaded).toBe(false);
	});
	it("reuses a destination document so a late acceptance preserves reopened input", async () => {
		const { library } = setup();
		const destination = { hubId: "one", sessionRef: "session" };
		const original = library.open(destination);
		original.edit("sent");
		await original.submit(async () => {
			library.open(destination).edit("composed after returning");
			return true;
		});
		expect(library.open(destination).getSnapshot().record).toEqual({
			draft: "composed after returning",
			unconfirmed: null,
		});
		expect(
			library.open({ ...destination, hubId: "two" }).getSnapshot().record.draft,
		).toBe("");
	});

	it("does not let a late completion or stale screen recreate a removed hub's drafts", async () => {
		const { library, repository } = setup();
		const destination = { hubId: "one", sessionRef: "session" };
		const document = library.open(destination);
		document.edit("sent");
		await document.submit(async () => {
			library.removeHub("one");
			document.edit("stale input");
			document.retry();
			return true;
		});
		expect(repository.read(destination)).toEqual({
			draft: "",
			unconfirmed: null,
		});
		expect(library.open(destination)).not.toBe(document);
		expect(library.open(destination).getSnapshot().record.draft).toBe("");
	});
});
