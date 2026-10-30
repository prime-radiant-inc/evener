import { afterEach, describe, expect, it } from "vitest";
import { DraftSubmissionLedgerSQLite } from "./draftSubmissionLedger";
import { DraftLibrary } from "./draftLibrary";
import { type DraftDestination, DraftRepository } from "./draftRepository";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

const databases: SqliteDoubleDatabase[] = [];
afterEach(() => {
	for (const db of databases.splice(0)) db.close();
});
function setup() {
	const { database: db, port } = openSqliteSyncDouble();
	databases.push(db);
	const repository = new DraftRepository(port);
	const { database: ledgerDb, port: ledgerPort } = openSqliteSyncDouble();
	databases.push(ledgerDb);
	let next = 0;
	const ledger = new DraftSubmissionLedgerSQLite(ledgerPort, {
		now: () => 1000,
	});
	return {
		library: new DraftLibrary(
			() => repository,
			() => `id-${++next}`,
			() => ledger,
		),
		repository,
		db,
		ledger,
	};
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

		// The destination screen can accept new input while the first submission
		// is still settling; that work must survive the completion.
		const destination = library.open(to);
		destination.edit("edited after the handoff");
		settle(true);
		await submitted;
		expect(repository.read(to)).toEqual({ draft: "edited after the handoff", unconfirmed: null });
		expect(destination.getSnapshot().record.draft).toBe("edited after the handoff");
		expect(repository.read(from)).toEqual({ draft: "", unconfirmed: null });
	});

	it("does not clear a newer destination capture when the retired submission completes", async () => {
		const { library, repository } = setup();
		const from = { hubId: "one", sessionRef: "old" };
		const to = { hubId: "one", sessionRef: "resumed" };
		const source = library.open(from);
		source.edit("slow message");
		let settle!: (sent: boolean) => void;
		const submitted = source.submit(
			() =>
				new Promise<boolean>((resolve) => {
					settle = resolve;
				}),
		);
		const token = repository.read(from).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing source capture token");
		expect(library.handoff(from, to)).toBe(true);

		const destination = library.open(to);
		expect(destination.clearMatchingUnconfirmedCapture(token)).toBe(true);
		await destination.submitText("newer message", async () => false);
		const newerToken = repository.read(to).unconfirmedCaptureToken;
		expect(newerToken).toBeDefined();
		expect(newerToken).not.toBe(token);

		settle(true);
		await submitted;
		expect(repository.read(to)).toMatchObject({
			unconfirmed: "newer message",
			unconfirmedCaptureToken: newerToken,
		});
	});

	it("leaves a destination with selection-only metadata untouched", () => {
		const { library, repository } = setup();
		const from = { hubId: "one", sessionRef: "old" };
		const to = { hubId: "one", sessionRef: "resumed" };
		repository.write(from, { draft: "edited recovery", unconfirmed: null });
		library.open(from).edit("edited recovery");
		repository.write(to, {
			draft: "",
			unconfirmed: null,
			composer: { skillNames: ["review"], commandNames: ["compact"] },
		});
		const destination = library.open(to);

		expect(library.handoff(from, to)).toBe(false);
		expect(destination.getSnapshot().record.composer).toEqual({
			skillNames: ["review"],
			commandNames: ["compact"],
		});
		expect(repository.read(from).draft).toBe("edited recovery");
	});

	it("retries a failed handoff after storage recovers and keeps the source draft meanwhile", () => {
		const { library, repository, db } = setup();
		const from = { hubId: "one", sessionRef: "old" };
		const to = { hubId: "one", sessionRef: "resumed" };
		repository.write(from, { draft: "edited recovery", unconfirmed: null });
		library.open(from).edit("edited recovery");
		db.exec("PRAGMA query_only = ON");

		expect(library.handoff(from, to)).toBe(false);
		expect(library.hasPendingHandoff(to)).toBe(true);
		expect(repository.read(from).draft).toBe("edited recovery");
		expect(library.open(from).getSnapshot().record.draft).toBe("edited recovery");

		db.exec("PRAGMA query_only = OFF");
		library.retryPendingHandoffs();
		expect(library.hasPendingHandoff(to)).toBe(false);
		expect(repository.read(to).draft).toBe("edited recovery");
		expect(repository.read(from)).toEqual({ draft: "", unconfirmed: null });
	});

	it("retries a destination load before deferring the handoff", () => {
		const { database: db, port } = openSqliteSyncDouble();
		databases.push(db);
		const repository = new DraftRepository(port);
		let failedReads = 2;
		const storage = {
			read: (destination: DraftDestination) => {
				if (destination.sessionRef === "resumed" && failedReads > 0) {
					failedReads -= 1;
					throw new Error("database is locked");
				}
				return repository.read(destination);
			},
			write: repository.write.bind(repository),
			transfer: repository.transfer.bind(repository),
			beginTransfer: repository.beginTransfer.bind(repository),
			outstandingTransfers: repository.outstandingTransfers.bind(repository),
			pendingTransfersTo: repository.pendingTransfersTo.bind(repository),
			transferById: repository.transferById.bind(repository),
			rerouteTransfers: repository.rerouteTransfers.bind(repository),
			latestCommittedTransferFrom: repository.latestCommittedTransferFrom.bind(repository),
			latestCommittedTransferFromCarrying: repository.latestCommittedTransferFromCarrying.bind(repository),
			latestTransferCarrying: repository.latestTransferCarrying.bind(repository),
			hasPendingTransferTo: repository.hasPendingTransferTo.bind(repository),
			forgetTransfer: repository.forgetTransfer.bind(repository),
			markTransferSettled: repository.markTransferSettled.bind(repository),
			removeHub: repository.removeHub.bind(repository),
			imageInputs: repository.imageInputs.bind(repository),
			refsWithDrafts: repository.refsWithDrafts.bind(repository),
		};
		const library = new DraftLibrary(() => storage);
		const from = { hubId: "one", sessionRef: "old" };
		const to = { hubId: "one", sessionRef: "resumed" };
		repository.write(from, { draft: "edited recovery", unconfirmed: null });
		library.open(from).edit("edited recovery");

		// The initial open and the retry inside handoff both fail to load.
		expect(library.handoff(from, to)).toBe(false);
		expect(library.hasPendingHandoff(to)).toBe(true);

		failedReads = 0;
		library.retryPendingHandoffs();
		expect(library.hasPendingHandoff(to)).toBe(false);
		expect(repository.read(to).draft).toBe("edited recovery");
	});

	it("forwards a pending submission through two handoffs", async () => {
		const { library, repository } = setup();
		const a = { hubId: "one", sessionRef: "a" };
		const b = { hubId: "one", sessionRef: "b" };
		const c = { hubId: "one", sessionRef: "c" };
		const source = library.open(a);
		source.edit("pending message");
		let settle!: (sent: boolean) => void;
		const submitted = source.submit(
			() =>
				new Promise<boolean>((resolve) => {
					settle = resolve;
				}),
		);
		const token = repository.read(a).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing source capture token");

		expect(library.handoff(a, b)).toBe(true);
		const middle = library.open(b);
		middle.edit("middle work");
		expect(library.handoff(b, c)).toBe(true);
		expect(repository.read(c)).toMatchObject({
			draft: "middle work",
			unconfirmed: "pending message",
			unconfirmedCaptureToken: token,
		});

		settle(true);
		await submitted;
		const final = library.open(c);
		expect(repository.read(c)).toEqual({ draft: "middle work", unconfirmed: null });
		expect(final.getSnapshot().record.unconfirmed).toBeNull();
		expect(repository.read(b)).toEqual({ draft: "", unconfirmed: null });
	});

	it("retires ordinary source mutations while retaining settlement forwarding", async () => {
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
		expect(library.handoff(from, to)).toBe(true);

		// A callback holding the retired document must not recreate its draft.
		source.edit("late edit");
		source.retry();
		source.addImage({ id: "late-image", marker: 7, mediaType: "image/png", data: "AQID" });
		let submittedAgain = false;
		await source.submit(async () => {
			submittedAgain = true;
			return true;
		});
		expect(submittedAgain).toBe(false);
		expect(source.canRestoreRecoveredDraft()).toBe(false);
		expect(repository.read(from)).toEqual({ draft: "", unconfirmed: null });

		settle(true);
		await submitted;
		expect(repository.read(to)).toEqual({ draft: "", unconfirmed: null });
	});

	it("invalidates a retired source when its hub is removed", () => {
		const { library, repository } = setup();
		const from = { hubId: "one", sessionRef: "old" };
		const to = { hubId: "one", sessionRef: "resumed" };
		repository.write(from, { draft: "sent", unconfirmed: null });
		const source = library.open(from);
		expect(library.handoff(from, to)).toBe(true);
		source.edit("late edit");

		library.removeHub("one");
		expect(source.getSnapshot().loaded).toBe(false);
		expect(repository.read(from)).toEqual({ draft: "", unconfirmed: null });
	});
});
