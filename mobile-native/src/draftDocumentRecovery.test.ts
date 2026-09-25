// The atomic draft-restore seam the recovery panel writes through: a
// rejected mutation's recovered text is restored into an empty composer in one
// savepointed repository write, so a failure can never leave the draft
// half-updated, and an existing draft (text or image) is never clobbered.

import { afterEach, describe, expect, it, vi } from "vitest";
import { DraftDocument } from "./draftDocument";
import type { DraftImage } from "./draftImages";
import { DraftRepository } from "./draftRepository";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

const databases: SqliteDoubleDatabase[] = [];
function setup() {
	const { database: db, port } = openSqliteSyncDouble();
	databases.push(db);
	const repository = new DraftRepository(port);
	const destination = { hubId: "studio", sessionRef: "local/session-42" };
	return {
		db,
		port,
		repository,
		destination,
		document: new DraftDocument(() => repository, destination),
	};
}
afterEach(() => {
	for (const db of databases.splice(0)) db.close();
});

describe("atomic recovery restore", () => {
	it("writes recovered text into an empty composer and persists it", () => {
		const { document, repository, destination } = setup();

		expect(document.restoreRecoveredDraft("recovered text")).toBe(true);
		expect(repository.read(destination)).toEqual({
			draft: "recovered text",
			unconfirmed: null,
		});
		expect(document.getSnapshot().record.draft).toBe("recovered text");
	});

	it("refuses to clobber a composer that already holds a draft", () => {
		const { document, repository, destination } = setup();
		document.edit("typed");

		expect(document.restoreRecoveredDraft("recovered text")).toBe(false);
		expect(repository.read(destination).draft).toBe("typed");
	});

	it("refuses when the loaded draft already references an image", () => {
		const write = vi.fn();
		const repository = {
			read: () => ({
				draft: "",
				unconfirmed: null,
				images: [{ id: "photo", marker: 1, mediaType: "image/png" }],
			}),
			write,
			imageInputs: () => [],
		};
		const document = new DraftDocument(() => repository, {
			hubId: "studio",
			sessionRef: "local/session-42",
		});

		expect(document.restoreRecoveredDraft("recovered text")).toBe(false);
		expect(write).not.toHaveBeenCalled();
	});

	it("preserves an uncertain submission's text while restoring recovered text", async () => {
		const { document, repository, destination } = setup();
		await document.submitText("submitted earlier", async () => false);

		expect(document.restoreRecoveredDraft("recovered text")).toBe(true);
		expect(repository.read(destination)).toEqual({
			draft: "recovered text",
			unconfirmed: "submitted earlier",
		});
	});

	it("reports whether the composer can accept a restore", () => {
		const { document } = setup();
		expect(document.canRestoreRecoveredDraft()).toBe(true);
		document.edit("typed");
		expect(document.canRestoreRecoveredDraft()).toBe(false);
	});

	it("reports not-restorable after a failed save, so a restore cannot be offered", () => {
		const { repository, destination } = setup();
		const failing = {
			read: (to: typeof destination) => repository.read(to),
			write: () => {
				throw new Error("save failed");
			},
			imageInputs: (to: typeof destination, images: DraftImage[]) =>
				repository.imageInputs(to, images),
		};
		const document = new DraftDocument(() => failing, destination);

		expect(document.restoreRecoveredDraft("recovered text")).toBe(false);
		expect(document.canRestoreRecoveredDraft()).toBe(false);
	});

	it("leaves the durable draft unchanged and surfaces an error when the write fails", () => {
		const { repository, destination } = setup();
		const failing = {
			read: (to: typeof destination) => repository.read(to),
			write: () => {
				throw new Error("save failed");
			},
			imageInputs: (to: typeof destination, images: DraftImage[]) =>
				repository.imageInputs(to, images),
		};
		const document = new DraftDocument(() => failing, destination);

		expect(document.restoreRecoveredDraft("recovered text")).toBe(false);
		expect(repository.read(destination)).toEqual({ draft: "", unconfirmed: null });
		expect(document.getSnapshot().error).not.toBeNull();
	});
});

describe("recovered restore hint", () => {
	it("distinguishes an occupied composer from a blocked draft", () => {
		const { document, repository, destination } = setup();
		expect(document.recoveredRestoreHint()).toBeNull();
		document.edit("typed");
		expect(document.recoveredRestoreHint()).toContain("current draft");

		const failing = {
			read: () => ({ draft: "", unconfirmed: null }),
			write: () => {
				throw new Error("save failed");
			},
			imageInputs: (to: typeof destination, images: DraftImage[]) =>
				repository.imageInputs(to, images),
		};
		const blocked = new DraftDocument(() => failing, destination);
		expect(blocked.restoreRecoveredDraft("recovered")).toBe(false);
		const hint = blocked.recoveredRestoreHint();
		expect(hint).toContain("Retry saving");
		expect(hint).not.toContain("current draft");
	});
});
