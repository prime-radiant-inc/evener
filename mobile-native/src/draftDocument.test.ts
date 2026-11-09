import { afterEach, describe, expect, it } from "vitest";
import { MutationNotSentError } from "./conversation/state/conversationMutation";
import { DraftDocument } from "./draftDocument";
import { DraftRepository } from "./draftRepository";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

const databases: SqliteDoubleDatabase[] = [];
function setup() {
	const { database: db, port } = openSqliteSyncDouble();
	databases.push(db);
	const repository = new DraftRepository(port);
	const destination = { hubId: "studio", sessionRef: "local/session-42" };
	let nextCapture = 0;
	return {
		db,
		repository,
		destination,
		document: new DraftDocument(
			() => repository,
			destination,
			undefined,
			() => `draft-document-capture-${++nextCapture}`,
		),
	};
}
afterEach(() => {
	for (const db of databases.splice(0)) db.close();
});

describe("durable draft lifecycle", () => {
	it("replaces text and images together while preserving an uncertain submission", async () => {
		const { document, repository, destination } = setup();
		document.edit("draft");
		document.addImage({
			id: "photo",
			marker: 1,
			mediaType: "image/png",
			data: "AQID",
		});
		document.replaceDraft("/goal objective");
		expect(repository.read(destination)).toEqual({
			draft: "/goal objective",
			unconfirmed: null,
		});
		await document.submit(async () => false);
		document.replaceDraft("replacement");
		expect(repository.read(destination).unconfirmed).toBe("/goal objective");
		expect(repository.read(destination).draft).toBe("");
	});
	it("sends nothing for an empty draft", async () => {
		const { document, repository, destination } = setup();
		let calls = 0;
		await document.submit(async () => {
			calls++;
			return true;
		});
		expect(calls).toBe(0);
		expect(repository.read(destination).unconfirmed).toBeNull();
	});
	it("rejects a duplicate image identity without changing the existing draft", () => {
		const { document } = setup();
		const image = {
			id: "photo",
			marker: 1,
			mediaType: "image/png",
			data: "AQID",
		};
		document.addImage(image);
		const before = document.getSnapshot().record;
		expect(() => document.addImage({ ...image, data: "different" })).toThrow();
		expect(document.getSnapshot().record).toEqual(before);
		expect(document.getSnapshot().error).toBeNull();
	});

	it("keeps images added while a submission is in flight and removes only their own markers", async () => {
		const { document, repository, destination } = setup();
		const first = {
			id: "first",
			marker: 1,
			mediaType: "image/png",
			data: "AQID",
		};
		const second = {
			id: "second",
			marker: 2,
			mediaType: "image/png",
			data: "BAUG",
		};
		document.addImage(first);
		await document.submit(async (_text, images) => {
			expect(images).toHaveLength(1);
			document.edit("newer ");
			document.addImage(second);
			return true;
		});
		expect(repository.read(destination).images?.map((image) => image.id)).toEqual(["second"]);
		expect(() => repository.imageInputs(destination, [first])).toThrow();
		document.removeImage("second");
		expect(repository.read(destination)).toEqual({
			draft: "newer ",
			unconfirmed: null,
		});
		expect(() => repository.imageInputs(destination, [second])).toThrow();
	});

	it("retains a failed image save for explicit retry without dispatching incomplete bytes", async () => {
		const { db, document, repository, destination } = setup();
		db.exec("PRAGMA query_only = ON");
		document.addImage({
			id: "photo",
			marker: 1,
			mediaType: "image/png",
			data: "AQID",
		});
		expect(document.getSnapshot().record.images?.[0]?.id).toBe("photo");
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
		expect(repository.imageInputs(destination, repository.read(destination).images ?? [])[0]?.data).toBe("AQID");
	});

	it("checkpoints image-only input before dispatch and retains it through uncertain recovery", async () => {
		const { repository, destination } = setup();
		const image = { id: "photo", marker: 1, mediaType: "image/png" };
		repository.write(destination, { draft: "", unconfirmed: null, images: [image] }, [{ ...image, data: "AQID" }]);
		const document = new DraftDocument(() => repository, destination);
		let sent = false;
		await document.submit(async (text, images) => {
			sent = true;
			expect(text).toBe("");
			expect(images).toEqual([{ marker: 1, mediaType: "image/png", data: "AQID" }]);
			expect(repository.read(destination).unconfirmedImages).toEqual([image]);
			return false;
		});
		expect(sent).toBe(true);
		const reopened = new DraftDocument(() => repository, destination);
		expect(reopened.getSnapshot().record.unconfirmed).toBe("");
		reopened.restore();
		expect(repository.read(destination)).toEqual({
			draft: "",
			unconfirmed: null,
			images: [image],
		});
		await reopened.submit(async () => true);
		expect(() => repository.imageInputs(destination, [image])).toThrow();
	});

	it("persists re-issued attachments through an uncertain retry, so restore recovers them", async () => {
		const { repository, destination } = setup();
		repository.write(destination, { draft: "untouched", unconfirmed: null }, []);
		const document = new DraftDocument(() => repository, destination);
		const reissued = [{ marker: 1, mediaType: "image/png", data: "AQID", name: "plot.png" }];
		await document.submitText("retry this", async () => false, reissued);
		// The uncertain record carries the re-issued images as references, with
		// their bytes in storage: a crash or a refused send still recovers the
		// whole input, not text with orphaned markers.
		const record = repository.read(destination);
		expect(record.unconfirmed).toBe("retry this");
		const [reference] = record.unconfirmedImages ?? [];
		expect(reference?.marker).toBe(1);
		expect(reference?.id).toBeTruthy();
		const reopened = new DraftDocument(() => repository, destination);
		expect(reopened.getSnapshot().record.unconfirmedImages?.length).toBe(1);
		const preview = reopened.imagePreviews(reopened.getSnapshot().record.unconfirmedImages ?? []);
		expect(preview[0]).toMatchObject({ marker: 1, data: "AQID", name: "plot.png" });
	});

	it("gives each retry's re-issued images their own identity, even with the same marker and length", async () => {
		const { repository, destination } = setup();
		repository.write(destination, { draft: "", unconfirmed: null }, []);
		const document = new DraftDocument(() => repository, destination);
		const reissued = [{ marker: 1, mediaType: "image/png", data: "AQID" }];
		await document.submitText("first try", async () => false, reissued);
		document.restore();
		// The composer now holds the first retry's recovered image; a second
		// failed turn retries with different bytes of the same length and
		// marker. Both must persist: one identity per re-issue.
		await document.submitText("second try", async () => false, reissued);
		const record = repository.read(destination);
		expect(record.unconfirmed).toBe("second try");
		expect(record.unconfirmedImages?.length).toBe(1);
		expect(record.images?.length).toBe(1);
		expect(new Set([...(record.images ?? []), ...(record.unconfirmedImages ?? [])].map((i) => i.id)).size).toBe(2);
	});

	it("a structured answer preserves ordinary images and manual restore does not replace newer images", async () => {
		const { repository, destination } = setup();
		const image = { id: "photo", marker: 1, mediaType: "image/png" };
		repository.write(destination, { draft: "", unconfirmed: null, images: [image] }, [{ ...image, data: "AQID" }]);
		const document = new DraftDocument(() => repository, destination);
		await document.submitText("answer", async () => false);
		expect(repository.read(destination).images).toEqual([image]);
		expect(repository.read(destination).unconfirmedImages).toBeUndefined();
		document.restore();
		expect(repository.read(destination).unconfirmed).toBe("answer");
		document.dismiss();
		expect(repository.imageInputs(destination, [image])).toHaveLength(1);
	});

	it("keeps exact text after a document is recreated", () => {
		const { document, repository, destination } = setup();
		const input = "  draft\nwith unicode 🦋  ";
		document.edit(input);
		expect(new DraftDocument(() => repository, destination).getSnapshot().record.draft).toBe(input);
	});

	it("checkpoints before sending and preserves newer text when accepted", async () => {
		const { document, repository, destination } = setup();
		document.edit("submitted");
		await document.submit(async (text) => {
			expect(text).toBe("submitted");
			expect(repository.read(destination)).toEqual({
				draft: "",
				unconfirmed: "submitted",
				unconfirmedCaptureToken: "draft-document-capture-1",
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
			unconfirmedCaptureToken: "draft-document-capture-1",
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
			unconfirmedCaptureToken: "draft-document-capture-1",
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
			unconfirmedCaptureToken: "draft-document-capture-1",
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
		unconfirmedCaptureToken: "draft-document-capture-1",
	});
});

describe("a mutation proven not sent", () => {
	it("clears only its own uncertainty marker and keeps the edited draft and images", async () => {
		const { document, repository, destination } = setup();
		document.edit("keep me");
		document.addImage({ id: "photo", marker: 1, mediaType: "image/png", data: "AQID" });
		const before = document.getSnapshot().record;
		const outcome = await document.submitText("continue", async () => {
			throw new MutationNotSentError("the session retired");
		});
		expect(outcome).toBe("not-sent");
		expect(document.getSnapshot().submitting).toBe(false);
		expect(repository.read(destination)).toEqual(before);
		expect(repository.imageInputs(destination, repository.read(destination).images ?? [])[0]?.data).toBe("AQID");
	});

	it("leaves an ordinary false outcome uncertain, as before", async () => {
		const { document, repository, destination } = setup();
		document.edit("keep me");
		const outcome = await document.submitText("continue", async () => false);
		expect(outcome).toBe(false);
		expect(repository.read(destination)).toEqual({
			draft: "keep me",
			unconfirmed: "continue",
			unconfirmedCaptureToken: "draft-document-capture-1",
		});
	});

	it("propagates an ordinary throw with its uncertainty marker intact", async () => {
		const { document, repository, destination } = setup();
		document.edit("keep me");
		await expect(
			document.submitText("continue", async () => {
				throw new Error("wire exploded");
			}),
		).rejects.toThrow("wire exploded");
		expect(document.getSnapshot().submitting).toBe(false);
		expect(repository.read(destination)).toEqual({
			draft: "keep me",
			unconfirmed: "continue",
			unconfirmedCaptureToken: "draft-document-capture-1",
		});
	});
});

describe("image insertion and selected labels", () => {
	function selectedDraft(text: string, offset: number) {
		const { repository, destination } = setup();
		repository.write(destination, {
			draft: text,
			unconfirmed: null,
			composer: {
				skillNames: ["inspect"],
				commandNames: ["inspect"],
				mentions: [
					{ kind: "skill", name: "inspect", offset },
					{ kind: "command", name: "inspect", offset },
				],
			},
		});
		return { document: new DraftDocument(() => repository, destination), repository, destination };
	}

	it("drops a label and its selections when the insertion splits it", () => {
		const { document, repository, destination } = selectedDraft("/inspect body", 0);

		document.addImage({ id: "photo", marker: 1, mediaType: "image/png", data: "AQID" }, 3);

		const record = repository.read(destination);
		expect(record.draft).toBe("/in[image 1]spect body");
		expect(record.composer?.mentions).toEqual([]);
		expect(record.composer?.skillNames).toEqual([]);
		expect(record.composer?.commandNames).toEqual([]);
	});

	it("keeps the label with the shifted offset when the insertion lands before it", async () => {
		const { document, repository, destination } = selectedDraft("/inspect body", 0);

		document.addImage({ id: "photo", marker: 1, mediaType: "image/png", data: "AQID" }, 0);

		const record = repository.read(destination);
		expect(record.draft).toBe("[image 1]/inspect body");
		expect(record.composer?.mentions).toEqual([
			{ kind: "skill", name: "inspect", offset: "[image 1]".length },
			{ kind: "command", name: "inspect", offset: "[image 1]".length },
		]);
		expect(record.composer?.skillNames).toEqual(["inspect"]);
		expect(record.composer?.commandNames).toEqual(["inspect"]);
		await document.submit(async (_text, _images, context) => {
			expect(context.composer?.mentions).toEqual([
				{ kind: "skill", name: "inspect", offset: "[image 1]".length },
				{ kind: "command", name: "inspect", offset: "[image 1]".length },
			]);
			return false;
		});
	});

	it("keeps the label at its offset when the insertion lands after it", () => {
		const { document, repository, destination } = selectedDraft("/inspect body", 0);

		document.addImage({ id: "photo", marker: 1, mediaType: "image/png", data: "AQID" }, "/inspect".length);

		const record = repository.read(destination);
		expect(record.draft).toBe("/inspect[image 1] body");
		expect(record.composer?.mentions).toEqual([
			{ kind: "skill", name: "inspect", offset: 0 },
			{ kind: "command", name: "inspect", offset: 0 },
		]);
		expect(record.composer?.skillNames).toEqual(["inspect"]);
		expect(record.composer?.commandNames).toEqual(["inspect"]);
	});
});
