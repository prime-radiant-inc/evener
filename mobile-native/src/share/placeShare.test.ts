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

const destination = { hubId: "studio", sessionRef: "local:coord" };

it("appends the shared text to the saved draft and keeps the old draft", () => {
	const document = new DraftDocument(() => repository, destination);
	document.edit("What I was already writing");
	expect(placeSharedText(document, "What I shared")).toBe(true);
	expect(document.getSnapshot().record.draft).toBe("What I was already writing\n\nWhat I shared");
	// Durable: a fresh document reads the same draft back.
	const reopened = new DraftDocument(() => repository, destination);
	expect(reopened.getSnapshot().record.draft).toBe("What I was already writing\n\nWhat I shared");
});

it("places the text alone when the draft was empty", () => {
	const document = new DraftDocument(() => repository, destination);
	expect(placeSharedText(document, "What I shared")).toBe(true);
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
		destination,
	);
	expect(placeSharedText(document, "What I shared")).toBe(false);
});
