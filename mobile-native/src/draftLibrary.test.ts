import { afterEach, describe, expect, it } from "vitest";
import { DraftLibrary } from "./draftLibrary";
import { DraftRepository } from "./draftRepository";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

const databases: SqliteDoubleDatabase[] = [];
afterEach(() => {
	for (const db of databases.splice(0)) db.close();
});
function setup() {
	const { database: db, port } = openSqliteSyncDouble();
	databases.push(db);
	const repository = new DraftRepository(port);
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
		expect(library.open({ ...destination, hubId: "two" }).getSnapshot().record.draft).toBe("");
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

	it("names a hub's sessions with drafts from its repository", () => {
		const { library } = setup();
		library.open({ hubId: "one", sessionRef: "typed" }).edit("half a thought");
		library.open({ hubId: "two", sessionRef: "other" }).edit("other hub");
		expect([...library.refsWithDrafts("one")]).toEqual(["typed"]);
	});

	it("hands edited recovery ownership and images to an already-open resumed identity", () => {
		const { library, repository } = setup();
		const from = { hubId: "one", sessionRef: "old" };
		const to = { hubId: "one", sessionRef: "resumed" };
		repository.write(
			from,
			{
				draft: "recovered [image 1]",
				unconfirmed: null,
				images: [{ id: "photo", marker: 1, mediaType: "image/png" }],
				composer: { skillNames: ["review"], commandNames: ["compact"], recoveryIds: ["source-a"] },
			},
			[{ id: "photo", marker: 1, mediaType: "image/png", data: "AQID" }],
		);
		library.open(from).edit("edited recovered [image 1]");
		// The resumed screen mounted before the handoff and is holding its empty
		// snapshot: the transfer must adopt into that live document.
		const destination = library.open(to);

		expect(library.handoff(from, to)).toBe(true);
		expect(destination.getSnapshot().record).toMatchObject({
			draft: "edited recovered [image 1]",
			unconfirmed: null,
			images: [{ id: "photo", marker: 1, mediaType: "image/png" }],
			composer: { skillNames: ["review"], commandNames: ["compact"], recoveryIds: ["source-a"] },
		});
		expect(repository.imageInputs(to, destination.getSnapshot().record.images ?? [])).toEqual([
			expect.objectContaining({ data: "AQID" }),
		]);
		expect(repository.read(from)).toEqual({ draft: "", unconfirmed: null });
		expect([...library.refsWithDrafts("one")]).toEqual(["resumed"]);
		expect(library.open(to)).toBe(destination);
	});

	it("keeps a pending submission's capture under the resumed identity and clears it on completion", async () => {
		const { library, repository } = setup();
		const from = { hubId: "one", sessionRef: "old" };
		const to = { hubId: "one", sessionRef: "resumed" };
		const source = library.open(from);
		source.edit("pending message");
		let settle!: (sent: boolean) => void;
		const submitted = source.submit(
			() =>
				new Promise<boolean>((resolve) => {
					settle = resolve;
				}),
		);
		const token = repository.read(from).unconfirmedCaptureToken;
		expect(token).toBeDefined();
		expect(repository.read(from).unconfirmed).toBe("pending message");

		expect(library.handoff(from, to)).toBe(true);
		expect(repository.read(to)).toMatchObject({
			unconfirmed: "pending message",
			unconfirmedCaptureToken: token,
		});

		settle(true);
		await submitted;
		expect(repository.read(to)).toEqual({ draft: "", unconfirmed: null });
		expect(repository.read(from)).toEqual({ draft: "", unconfirmed: null });
	});
});
