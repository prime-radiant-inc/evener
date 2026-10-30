import { afterEach, expect, it } from "vitest";
import { MAX_ATTACHMENT_BYTES } from "@evener/appwire-client";
import { DraftDocument } from "./draftDocument";
import { DraftRepository } from "./draftRepository";
import {
	CameraAccessDenied,
	type EncodedImage,
	ImageSelection,
	MAX_SOURCE_BYTES,
	type PickedImage,
} from "./imageSelection";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

const databases: SqliteDoubleDatabase[] = [];
afterEach(() => {
	for (const db of databases.splice(0)) db.close();
});
function setup(
	pick: () => Promise<PickedImage[]>,
	/** A string is the image as JPEG. */
	encode: (image: PickedImage) => Promise<string | EncodedImage>,
	capture: () => Promise<PickedImage[]> = async () => [],
) {
	const { database: db, port } = openSqliteSyncDouble();
	databases.push(db);
	const repository = new DraftRepository(port);
	const destination = { hubId: "hub", sessionRef: "session" };
	const document = new DraftDocument(() => repository, destination);
	let id = 0;
	return {
		document,
		selection: new ImageSelection(document, {
			pick,
			capture,
			encode: async (image) => {
				const encoded = await encode(image);
				return typeof encoded === "string" ? { data: encoded, mediaType: "image/jpeg" } : encoded;
			},
			id: () => `image-${++id}`,
		}),
	};
}
const photo = {
	uri: "file:///photo.jpg",
	name: "photo.jpg",
	type: "image/jpeg",
	size: 10,
};

it("takes a photo bigger than the limit on disk when the phone's encoding of it fits", async () => {
	// A camera photo is often over 8 MB as picked; the phone scales it down.
	const source = { ...photo, size: MAX_ATTACHMENT_BYTES * 3 };
	const { document, selection } = setup(
		async () => [source],
		async () => "AQID",
	);

	await selection.choose();

	expect(selection.getSnapshot().error).toBeNull();
	expect(document.getSnapshot().record.images).toHaveLength(1);
	expect(document.getSnapshot().record.images?.[0]?.mediaType).toBe("image/jpeg");
});

/** Picks `images`, counting the decodes the selection asks for. */
async function chooseCounting(images: PickedImage[]) {
	let decoded = 0;
	const { document, selection } = setup(
		async () => images,
		async () => {
			decoded++;
			return "AQID";
		},
	);
	await selection.choose();
	const { error, refusals } = selection.getSnapshot();
	return { decoded, attached: document.getSnapshot().record.images ?? [], error, refusals };
}

it("refuses a file over 25 MB before decoding it, and says what to do", async () => {
	const result = await chooseCounting([{ ...photo, name: "huge.jpg", size: MAX_SOURCE_BYTES + 1 }]);
	expect(result.decoded).toBe(0);
	expect(result.attached).toHaveLength(0);
	expect(result.refusals).toEqual([{ name: "huge.jpg", reason: "tooLarge" }]);
});

it("takes a file at exactly 25 MB", async () => {
	const result = await chooseCounting([{ ...photo, size: MAX_SOURCE_BYTES }]);
	expect(result.error).toBeNull();
	expect(result.attached).toHaveLength(1);
});

it("refuses a photo over 48 megapixels by the size the picker reports, before decoding it", async () => {
	const result = await chooseCounting([{ ...photo, name: "pano.jpg", width: 16000, height: 4000 }]);
	expect(result.decoded).toBe(0);
	expect(result.attached).toHaveLength(0);
	expect(result.refusals).toEqual([{ name: "pano.jpg", reason: "tooLarge" }]);
});

it("takes a full-frame 48 megapixel camera photo", async () => {
	const result = await chooseCounting([{ ...photo, width: 8064, height: 6048 }]);
	expect(result.error).toBeNull();
	expect(result.attached).toHaveLength(1);
});

it("takes a photo whose pixel size the picker doesn't know", async () => {
	const result = await chooseCounting([{ ...photo, width: 0, height: 0 }]);
	expect(result.error).toBeNull();
	expect(result.attached).toHaveLength(1);
});

it("rejects an encoded image over the server byte limit without persisting it", async () => {
	const source = { ...photo, size: MAX_ATTACHMENT_BYTES - 1 };
	const valid = { ...source, name: "valid.png" };
	const encoded = Buffer.alloc(MAX_ATTACHMENT_BYTES + 1).toString("base64");
	const { document, selection } = setup(
		async () => [source, valid],
		async (image) => (image.name === source.name ? encoded : "AQID"),
	);

	await selection.choose();

	expect(document.getSnapshot().record.images).toHaveLength(1);
	expect(document.getSnapshot().record.images?.[0]?.name).toBe(valid.name);
	expect(document.getSnapshot().record.images?.[0]?.marker).toBe(2);
	expect(document.getSnapshot().record.draft).toBe("[image 2]");
	expect(selection.getSnapshot().refusals).toEqual([{ name: source.name, reason: "tooLarge" }]);
	expect(selection.getSnapshot().pending).toHaveLength(0);
});

it("accepts an encoded image at the server byte limit", async () => {
	const source = { ...photo, size: MAX_ATTACHMENT_BYTES - 1 };
	const encoded = Buffer.alloc(MAX_ATTACHMENT_BYTES).toString("base64");
	const { document, selection } = setup(
		async () => [source],
		async () => encoded,
	);

	await selection.choose();

	expect(document.getSnapshot().record.images).toHaveLength(1);
	expect(document.imagePreviews()[0]?.data).toBe(encoded);
	expect(selection.getSnapshot().error).toBeNull();
});

it("accepts valid encoded images through the padded size boundary", async () => {
	const source = { ...photo, size: MAX_ATTACHMENT_BYTES - 1 };
	const sizes = [MAX_ATTACHMENT_BYTES - 2, MAX_ATTACHMENT_BYTES - 1, MAX_ATTACHMENT_BYTES];
	const picked = sizes.map((size) => ({
		...source,
		name: `image-${size}.png`,
	}));
	const { document, selection } = setup(
		async () => picked,
		async (image) => Buffer.alloc(Number(image.name.slice("image-".length, -".png".length))).toString("base64"),
	);

	await selection.choose();

	expect(document.getSnapshot().record.images).toHaveLength(3);
	expect(selection.getSnapshot().error).toBeNull();
});

it("enforces current-web type, count and source-size limits before encoding", async () => {
	let encoded = 0;
	const { document, selection } = setup(
		async () => [
			{ ...photo, type: "text/plain" },
			{ ...photo, size: MAX_SOURCE_BYTES + 1 },
			...Array.from({ length: 9 }, () => photo),
		],
		async () => {
			encoded++;
			return "AQID";
		},
	);
	await selection.choose();
	expect(encoded).toBe(8);
	expect(document.getSnapshot().record.images).toHaveLength(8);
	expect(selection.getSnapshot().error).not.toBeNull();
	expect(selection.getSnapshot().busy).toBe(false);
});

it("removing a pending image prevents its late encoder result from entering the draft", async () => {
	let finish!: (data: string) => void;
	let started!: () => void;
	const encoding = new Promise<void>((resolve) => {
		started = resolve;
	});
	const { document, selection } = setup(
		async () => [photo],
		() => {
			started();
			return new Promise((resolve) => {
				finish = resolve;
			});
		},
	);
	const choosing = selection.choose();
	await encoding;
	selection.remove("image-1");
	finish("AQID");
	await choosing;
	expect(document.getSnapshot().record).toEqual({
		draft: "",
		unconfirmed: null,
	});
	expect(selection.getSnapshot().pending).toHaveLength(0);
});

it("cancels a destination's selection without applying its late result", async () => {
	let finish!: (images: PickedImage[]) => void;
	const { document, selection } = setup(
		() =>
			new Promise((resolve) => {
				finish = resolve;
			}),
		async () => "AQID",
	);
	const choosing = selection.choose();
	selection.cancel();
	finish([photo]);
	await choosing;
	expect(document.getSnapshot().record.images).toBeUndefined();
	expect(selection.getSnapshot().busy).toBe(false);
});

it("retains current text while decoding and does not reuse a removed marker", async () => {
	const { document, selection } = setup(
		async () => [photo],
		async () => {
			document.edit("newer ");
			return "AQID";
		},
	);
	await selection.choose();
	const first = document.getSnapshot().record.images?.[0];
	expect(first?.marker).toBe(1);
	document.removeImage(first?.id ?? "");
	await selection.choose();
	expect(document.getSnapshot().record.images?.[0]?.marker).toBe(2);
	expect(document.getSnapshot().record.draft.startsWith("newer ")).toBe(true);
});

it("reserves literal and current image markers before allocating a selection", async () => {
	const { document, selection } = setup(
		async () => [photo],
		async () => "AQID",
	);
	document.edit("literal [image 1] and [image 3]");
	document.addImage({ id: "existing", marker: 2, mediaType: "image/png", data: "BAUG" });

	await selection.choose();

	expect(document.getSnapshot().record.images?.map((image) => image.marker)).toEqual([2, 4]);
	expect(document.getSnapshot().record.draft).toBe("literal [image 1] and [image 3][image 2][image 4]");
});

it("reallocates a reserved marker claimed by text while encoding is pending", async () => {
	let finish!: (data: string) => void;
	let started!: () => void;
	const encoding = new Promise<void>((resolve) => {
		started = resolve;
	});
	const { document, selection } = setup(
		async () => [photo],
		() => {
			started();
			return new Promise((resolve) => {
				finish = resolve;
			});
		},
	);
	const choosing = selection.choose();
	await encoding;
	document.edit("typed [image 1]");
	finish("AQID");
	await choosing;

	const record = document.getSnapshot().record;
	expect(record.draft).toBe("typed [image 1][image 2]");
	expect(record.images?.map((image) => [image.id, image.marker])).toEqual([["image-1", 2]]);
	expect(document.imagePreviews()[0]?.data).toBe("AQID");
});

it("reserves unconfirmed and pending image markers for the whole selection", async () => {
	const { document, selection } = setup(
		async () => [photo, { ...photo, name: "second.jpg" }],
		async () => "AQID",
	);
	document.edit("literal [image 1]");
	document.replaceDraft("literal [image 1]");
	await document.submitText("sending [image 5]", async () => false, [
		{ marker: 5, mediaType: "image/png", data: "BAUG" },
	]);
	await selection.choose();

	expect(document.getSnapshot().record.images?.map((image) => image.marker)).toEqual([6, 7]);
	expect(document.getSnapshot().record.unconfirmedImages?.map((image) => image.marker)).toEqual([5]);
});

it("reallocates a pending marker when another tile claims it during encoding", async () => {
	let finish!: (data: string) => void;
	let started!: () => void;
	const encoding = new Promise<void>((resolve) => {
		started = resolve;
	});
	const { document, selection } = setup(
		async () => [photo],
		() => {
			started();
			return new Promise((resolve) => {
				finish = resolve;
			});
		},
	);
	const choosing = selection.choose();
	await encoding;
	document.addImage({ id: "new-tile", marker: 1, mediaType: "image/png", data: "BAUG" });
	finish("AQID");
	await choosing;

	expect(document.getSnapshot().record.images?.map((image) => [image.id, image.marker])).toEqual([
		["new-tile", 1],
		["image-1", 2],
	]);
	expect(document.imagePreviews().map((image) => image.data)).toEqual(["BAUG", "AQID"]);
});

it("adds a photo the camera took through the same encoding and markers", async () => {
	const shot = { ...photo, name: "camera.jpg" };
	const { document, selection } = setup(
		async () => {
			throw new Error("the library is not asked for a camera photo");
		},
		async () => "AQID",
		async () => [shot],
	);

	await selection.choose("camera");

	expect(document.getSnapshot().record.images).toHaveLength(1);
	expect(document.getSnapshot().record.images?.[0]?.name).toBe("camera.jpg");
	expect(document.getSnapshot().record.images?.[0]?.marker).toBe(1);
	expect(document.getSnapshot().record.draft).toBe("[image 1]");
	expect(selection.getSnapshot().error).toBeNull();
});

it("says how to turn the camera on when its permission is refused", async () => {
	const { document, selection } = setup(
		async () => [photo],
		async () => "AQID",
		async () => {
			throw new CameraAccessDenied();
		},
	);

	await selection.choose("camera");

	expect(document.getSnapshot().record.images).toBeUndefined();
	expect(selection.getSnapshot().refusals).toEqual([{ name: null, reason: "cameraDenied" }]);
	expect(selection.getSnapshot().busy).toBe(false);
});

it("keeps the type the phone encoded an image as, a screenshot's PNG included", async () => {
	const { document, selection } = setup(
		async () => [{ ...photo, name: "shot.png", type: "image/png" }],
		async () => ({ data: "UE5H", mediaType: "image/png" }),
	);
	await selection.choose();
	expect(document.getSnapshot().record.images?.[0]).toMatchObject({ name: "shot.png", mediaType: "image/png" });
});

it("says an image the phone couldn't encode couldn't be processed, and attaches the rest", async () => {
	const { document, selection } = setup(
		async () => [{ ...photo, name: "broken.heic" }, photo],
		async (image) => {
			if (image.name === "broken.heic") throw new Error("could not decode");
			return "AQID";
		},
	);
	await selection.choose();
	expect(selection.getSnapshot().refusals).toEqual([{ name: "broken.heic", reason: "notPrepared" }]);
	expect(document.getSnapshot().record.images).toHaveLength(1);
});

it("says why each picked file can't be attached, one line each (#3166)", async () => {
	const { selection } = setup(
		async () => [
			{ ...photo, name: "notes.txt", type: "text/plain" },
			{ ...photo, name: "unread.jpg", size: Number.NaN },
			...Array.from({ length: 8 }, (_, index) => ({ ...photo, name: `photo-${index}.jpg` })),
			{ ...photo, name: "ninth.jpg" },
		],
		async () => "AQID",
	);
	await selection.choose();
	const { error, refusals } = selection.getSnapshot();
	expect(refusals).toEqual([
		{ name: "notes.txt", reason: "notImage" },
		{ name: "unread.jpg", reason: "sizeUnread" },
		{ name: "ninth.jpg", reason: "tooMany" },
	]);
	expect(error?.split("\n")).toHaveLength(refusals.length);
});
