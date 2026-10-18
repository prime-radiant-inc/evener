import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, expect, it } from "vitest";
import { DraftDocument } from "../draftDocument";
import { DraftRepository } from "../draftRepository";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "../sqliteSync.testkit";
import { placeSharedText } from "./placeShare";

let directory: string;
let database: SqliteDoubleDatabase;
let repository: DraftRepository;

beforeEach(() => {
	directory = mkdtempSync(join(tmpdir(), "evener-share-draft-"));
	const opened = openSqliteSyncDouble(join(directory, "drafts.sqlite"));
	database = opened.database;
	repository = new DraftRepository(opened.port);
});

afterEach(() => {
	database.close();
	rmSync(directory, { recursive: true, force: true });
});

const destinationKey = { hubId: "studio", sessionRef: "local:coord" };

it("appends the shared text to the saved draft and keeps the old draft", () => {
	const document = new DraftDocument(() => repository, destinationKey);
	document.edit("What I was already writing");
	expect(placeSharedText(document, { id: "s1", text: "What I shared" })).toBe(true);
	expect(document.getSnapshot().record.draft).toBe("What I was already writing\n\nWhat I shared");
	// Durable: a fresh document reads the same draft back.
	const reopened = new DraftDocument(() => repository, destinationKey);
	expect(reopened.getSnapshot().record.draft).toBe("What I was already writing\n\nWhat I shared");
});

it("places the text alone when the draft was empty", () => {
	const document = new DraftDocument(() => repository, destinationKey);
	expect(placeSharedText(document, { id: "s1", text: "What I shared" })).toBe(true);
	expect(document.getSnapshot().record.draft).toBe("What I shared");
});

it("reports a placement that did not land when the draft cannot load", () => {
	const document = new DraftDocument(
		() => ({
			read: () => {
				throw new Error("draft storage unavailable");
			},
			write: () => {
				throw new Error("draft storage unavailable");
			},
			imageInputs: () => [],
		}),
		destinationKey,
	);
	expect(placeSharedText(document, { id: "s1", text: "What I shared" })).toBe(false);
});

it("retries a failed save with the same merge, appending the share once", () => {
	let failWrites = true;
	const { database: db, port } = openSqliteSyncDouble();
	try {
		const repository = new DraftRepository(port);
		const flaky = {
			read: (destination: typeof destinationKey) => repository.read(destination),
			write: (destination: typeof destinationKey, record: Parameters<DraftRepository["write"]>[1]) => {
				if (failWrites) throw new Error("disk full");
				repository.write(destination, record);
			},
			imageInputs: repository.imageInputs.bind(repository),
		};
		const document = new DraftDocument(() => flaky, destinationKey);
		document.edit("What I was writing");
		// The first edit itself could not save; clear its error so this test is
		// about the share's retry.
		failWrites = false;
		document.retry();
		failWrites = true;
		expect(placeSharedText(document, { id: "s1", text: "What I shared" })).toBe(false);
		failWrites = false;
		expect(placeSharedText(document, { id: "s1", text: "What I shared" })).toBe(true);
		expect(document.getSnapshot().record.draft).toBe("What I was writing\n\nWhat I shared");
	} finally {
		db.close();
	}
});

it("places a second share's own text after a first share's save failed", () => {
	let failWrites = true;
	const { database: db, port } = openSqliteSyncDouble();
	try {
		const repository = new DraftRepository(port);
		const flaky = {
			read: (destination: typeof destinationKey) => repository.read(destination),
			write: (destination: typeof destinationKey, record: Parameters<DraftRepository["write"]>[1]) => {
				if (failWrites) throw new Error("disk full");
				repository.write(destination, record);
			},
			imageInputs: repository.imageInputs.bind(repository),
		};
		// A readable draft that cannot be saved: the first share's merge stays
		// in the document, unsaved.
		failWrites = false;
		const document = new DraftDocument(() => flaky, destinationKey);
		failWrites = true;
		expect(placeSharedText(document, { id: "first", text: "First share" })).toBe(false);
		failWrites = false;
		expect(placeSharedText(document, { id: "second", text: "Second share" })).toBe(true);
		expect(document.getSnapshot().record.draft).toContain("Second share");
	} finally {
		db.close();
	}
});
