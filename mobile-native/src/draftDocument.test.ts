import { DatabaseSync } from "node:sqlite";
import { afterEach, describe, expect, it } from "vitest";
import { DraftDocument } from "./draftDocument";
import { type DraftDatabase, DraftRepository } from "./draftRepository";

const databases: DatabaseSync[] = [];
function setup() {
	const db = new DatabaseSync(":memory:");
	databases.push(db);
	const adapter: DraftDatabase = {
		execSync: (sql) => db.exec(sql),
		runSync: (sql, ...params) => db.prepare(sql).run(...params),
		getFirstSync: <T>(sql: string, ...params: string[]) =>
			(db.prepare(sql).get(...params) as T | undefined) ?? null,
	};
	const repository = new DraftRepository(adapter);
	const destination = { hubId: "studio", sessionRef: "local/session-42" };
	return {
		db,
		repository,
		destination,
		document: new DraftDocument(() => repository, destination),
	};
}
afterEach(() => {
	for (const db of databases.splice(0)) db.close();
});

describe("durable draft lifecycle", () => {
	it("keeps exact text after a document is recreated", () => {
		const { document, repository, destination } = setup();
		const input = "  draft\nwith unicode 🦋  ";
		document.edit(input);
		expect(
			new DraftDocument(() => repository, destination).getSnapshot().record
				.draft,
		).toBe(input);
	});

	it("checkpoints before sending and preserves newer text when accepted", async () => {
		const { document, repository, destination } = setup();
		document.edit("submitted");
		await document.submit(async (text) => {
			expect(text).toBe("submitted");
			expect(repository.read(destination)).toEqual({
				draft: "",
				unconfirmed: "submitted",
			});
			document.edit("newer draft");
			return true;
		});
		expect(repository.read(destination)).toEqual({
			draft: "newer draft",
			unconfirmed: null,
		});
	});

	it("does not replay uncertain input after recreation or overwrite a newer draft during recovery", async () => {
		const { document, repository, destination } = setup();
		document.edit("uncertain");
		await document.submit(async () => {
			document.edit("newer");
			return false;
		});
		const reopened = new DraftDocument(() => repository, destination);
		expect(reopened.getSnapshot().record).toEqual({
			draft: "newer",
			unconfirmed: "uncertain",
		});
		reopened.restore();
		expect(reopened.getSnapshot().record.draft).toBe("newer");
		let called = false;
		await reopened.submit(async () => {
			called = true;
			return true;
		});
		expect(called).toBe(false);
		reopened.edit("");
		reopened.restore();
		expect(repository.read(destination)).toEqual({
			draft: "uncertain",
			unconfirmed: null,
		});
	});

	it("keeps a durable uncertain record if the operation rejects", async () => {
		const { document, repository, destination } = setup();
		document.edit("input");
		await expect(
			document.submit(async () => {
				throw new Error("transport ended");
			}),
		).rejects.toThrow();
		expect(repository.read(destination)).toEqual({
			draft: "",
			unconfirmed: "input",
		});
		expect(document.getSnapshot().submitting).toBe(false);
	});

	it("preserves unsaved input and blocks transport when SQLite cannot checkpoint", async () => {
		const { db, document } = setup();
		db.exec("PRAGMA query_only = ON");
		document.edit("do not lose me");
		expect(document.getSnapshot().record.draft).toBe("do not lose me");
		expect(document.getSnapshot().error).not.toBeNull();
		let sent = false;
		await document.submit(async () => {
			sent = true;
			return true;
		});
		expect(sent).toBe(false);
		db.exec("PRAGMA query_only = OFF");
		document.retry();
		expect(document.getSnapshot().error).toBeNull();
		db.exec("PRAGMA query_only = ON");
		await expect(
			document.submit(async () => {
				sent = true;
				return true;
			}),
		).rejects.toThrow();
		expect(sent).toBe(false);
		expect(document.getSnapshot().record.draft).toBe("do not lose me");
	});

	it("does not overwrite unread data after a load error", () => {
		const { db, repository, destination } = setup();
		repository.write(destination, { draft: "saved", unconfirmed: null });
		db.exec("ALTER TABLE drafts RENAME TO unavailable_drafts");
		const document = new DraftDocument(() => repository, destination);
		expect(document.getSnapshot().loaded).toBe(false);
		document.edit("replacement");
		expect(document.getSnapshot().record.draft).toBe("");
		db.exec("ALTER TABLE unavailable_drafts RENAME TO drafts");
		document.retry();
		expect(document.getSnapshot().record.draft).toBe("saved");
	});
});

it("checkpoints a structured reply without consuming the ordinary draft", async () => {
	const { document, repository, destination } = setup();
	document.edit("ordinary draft");
	await document.submitText("structured reply", async (text) => {
		expect(text).toBe("structured reply");
		expect(repository.read(destination)).toEqual({
			draft: "ordinary draft",
			unconfirmed: "structured reply",
		});
		return true;
	});
	expect(repository.read(destination)).toEqual({
		draft: "ordinary draft",
		unconfirmed: null,
	});
});
it("retains uncertain structured replies and prevents a second dispatch", async () => {
	const { document, repository, destination } = setup();
	document.edit("ordinary draft");
	await document.submitText("structured reply", async () => false);
	const reopened = new DraftDocument(() => repository, destination);
	let calls = 0;
	await reopened.submitText("another reply", async () => {
		calls++;
		return true;
	});
	expect(calls).toBe(0);
	expect(reopened.getSnapshot().record).toEqual({
		draft: "ordinary draft",
		unconfirmed: "structured reply",
	});
});
