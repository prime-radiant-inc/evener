import { afterEach, expect, it } from "vitest";
import { DraftDocument } from "./draftDocument";
import { DraftRepository } from "./draftRepository";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";
import { buildComposerInput } from "@evener/appwire-client";

const databases: SqliteDoubleDatabase[] = [];
function setup() {
	const { database, port } = openSqliteSyncDouble();
	databases.push(database);
	const repository = new DraftRepository(port);
	const destination = { hubId: "hub", sessionRef: "session" };
	return { database, repository, destination, document: new DraftDocument(() => repository, destination) };
}
afterEach(() => {
	for (const database of databases.splice(0)) database.close();
});

it("shifts mention offsets around a multi-digit marker using UTF-16 positions", () => {
	const { repository, destination } = setup();
	repository.write(
		destination,
		{
			draft: "😀/before /after",
			unconfirmed: null,
			composer: {
				skillNames: ["before"],
				commandNames: ["after"],
				recoveryIds: ["keep-recovery"],
				mentions: [
					{ kind: "skill", name: "before", offset: 2 },
					{ kind: "command", name: "after", offset: 10 },
				],
			},
		},
		[],
	);
	const document = new DraftDocument(() => repository, destination);
	const image = { id: "tile", marker: 12, mediaType: "image/png", data: "AQID" };
	document.addImage(image, 9);

	expect(repository.read(destination)).toMatchObject({
		draft: "😀/before[image 12] /after",
		images: [{ id: "tile", marker: 12 }],
		composer: {
			skillNames: ["before"],
			commandNames: ["after"],
			recoveryIds: ["keep-recovery"],
			mentions: [
				{ kind: "skill", name: "before", offset: 2 },
				{ kind: "command", name: "after", offset: 20 },
			],
		},
	});
	document.removeImage("tile");
	expect(repository.read(destination)).toMatchObject({
		draft: "😀/before /after",
		composer: {
			skillNames: ["before"],
			commandNames: ["after"],
			recoveryIds: ["keep-recovery"],
			mentions: [
				{ kind: "skill", name: "before", offset: 2 },
				{ kind: "command", name: "after", offset: 10 },
			],
		},
	});
});

it("removing an image prunes a selected label that the shifted text no longer contains", async () => {
	const { repository, destination } = setup();
	repository.write(
		destination,
		{
			draft: "/skill[image 1]Extra",
			unconfirmed: null,
			images: [{ id: "tile", marker: 1, mediaType: "image/png" }],
			composer: {
				skillNames: ["skill"],
				mentions: [{ kind: "skill", name: "skill", offset: 0 }],
			},
		},
		[{ id: "tile", marker: 1, mediaType: "image/png", data: "AQID" }],
	);
	const document = new DraftDocument(() => repository, destination);
	document.removeImage("tile");
	const stored = repository.read(destination);
	expect(stored).toMatchObject({ draft: "/skillExtra", composer: { skillNames: [], mentions: [] } });
	let submitted: ReturnType<typeof buildComposerInput> | undefined;
	await document.submit(async (text, images, context) => {
		submitted = buildComposerInput(
			text,
			images,
			context.composer?.skillNames ?? [],
			context.composer?.commandNames ?? [],
			context.composer?.mentions,
		);
		return false;
	});
	expect(submitted).toEqual([{ type: "text", text: "/skillExtra" }]);
});

it("removing an image keeps a selected label that survives after the marker", async () => {
	const { repository, destination } = setup();
	repository.write(
		destination,
		{
			draft: "[image 1] /inspect",
			unconfirmed: null,
			images: [{ id: "tile", marker: 1, mediaType: "image/png" }],
			composer: {
				skillNames: ["inspect"],
				mentions: [{ kind: "skill", name: "inspect", offset: 10 }],
			},
		},
		[{ id: "tile", marker: 1, mediaType: "image/png", data: "AQID" }],
	);
	const document = new DraftDocument(() => repository, destination);
	document.removeImage("tile");
	const stored = repository.read(destination);
	expect(stored).toMatchObject({
		draft: " /inspect",
		composer: { skillNames: ["inspect"], mentions: [{ kind: "skill", name: "inspect", offset: 1 }] },
	});
	let submitted: ReturnType<typeof buildComposerInput> | undefined;
	await document.submit(async (text, images, context) => {
		submitted = buildComposerInput(
			text,
			images,
			context.composer?.skillNames ?? [],
			context.composer?.commandNames ?? [],
			context.composer?.mentions,
		);
		return false;
	});
	expect(submitted?.[0]).toMatchObject({ type: "text", text: " /inspect" });
	expect(submitted).toContainEqual({ type: "skill", name: "inspect" });
});

it("reallocates a marker claimed by a literal or another tile and reopens with original image bytes", () => {
	const { document, repository, destination } = setup();
	document.edit("literal [image 4]");
	document.addImage({ id: "existing", marker: 7, mediaType: "image/png", data: "BAUG" });
	const incoming = { id: "incoming", marker: 4, mediaType: "image/jpeg" as const, name: "photo.jpg", data: "AQID" };
	document.addImage(incoming);

	expect(document.getSnapshot().record.draft).toBe("literal [image 4][image 7][image 8]");
	expect(repository.read(destination).images?.map((image) => [image.id, image.marker])).toEqual([
		["existing", 7],
		["incoming", 8],
	]);
	expect(
		repository.imageInputs(destination, repository.read(destination).images ?? []).map((image) => image.data),
	).toEqual(["BAUG", "AQID"]);
	const reopened = new DraftDocument(() => repository, destination);
	expect(reopened.getSnapshot().record.images?.map((image) => [image.id, image.marker])).toEqual([
		["existing", 7],
		["incoming", 8],
	]);
	expect(reopened.imagePreviews().map((image) => image.data)).toEqual(["BAUG", "AQID"]);
});

it("retains reallocated image bytes through a failed save and explicit retry", () => {
	const { database, document, repository, destination } = setup();
	document.edit("[image 1]");
	database.exec("PRAGMA query_only = ON");
	document.addImage({ id: "incoming", marker: 1, mediaType: "image/png", data: "AQID" });
	expect(document.getSnapshot().record.images?.[0]).toMatchObject({ id: "incoming", marker: 2 });
	expect(document.getSnapshot().error).not.toBeNull();
	database.exec("PRAGMA query_only = OFF");
	document.retry();
	expect(repository.read(destination).images?.[0]).toMatchObject({ id: "incoming", marker: 2 });
	expect(repository.imageInputs(destination, repository.read(destination).images ?? [])[0]?.data).toBe("AQID");
});
