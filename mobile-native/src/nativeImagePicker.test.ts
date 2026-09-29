import { MAX_ATTACHMENT_BYTES } from "@evener/appwire-client";
import { beforeEach, expect, it, vi } from "vitest";

// A stand-in for expo-image-manipulator that records what the picker asks of
// it: each render, each context's resize, each save's options, and every
// native object it lets go of. `fail` makes a render or a save throw.
const manipulator = vi.hoisted(() => {
	const state = {
		source: { width: 0, height: 0 },
		renders: 0,
		resizes: [] as unknown[],
		saves: [] as unknown[],
		live: 0,
		deleted: [] as string[],
		read: [] as string[],
		fail: null as null | "render" | "save",
	};
	return {
		state,
		manipulate() {
			state.live++;
			let size = { ...state.source };
			return {
				resize(to: { width?: number; height?: number }) {
					state.resizes.push(to);
					const scale = to.width ? to.width / size.width : (to.height ?? size.height) / size.height;
					size = { width: Math.round(size.width * scale), height: Math.round(size.height * scale) };
				},
				async renderAsync() {
					state.renders++;
					if (state.fail === "render") throw new Error("could not decode");
					state.live++;
					return {
						...size,
						async saveAsync(options: unknown) {
							if (state.fail === "save") throw new Error("disk full");
							state.saves.push({ ...size, options });
							return { uri: `file:///cache/${state.saves.length}.jpg`, base64: "AQID" };
						},
						release() {
							state.live--;
						},
					};
				},
				release() {
					state.live--;
				},
			};
		},
	};
});

vi.mock("expo-image-manipulator", () => ({
	ImageManipulator: { manipulate: manipulator.manipulate },
	SaveFormat: { JPEG: "jpeg", PNG: "png" },
}));
vi.mock("expo-file-system", () => ({
	File: class {
		constructor(readonly uri: string) {}
		async base64() {
			manipulator.state.read.push(this.uri);
			return "UE5H";
		}
		delete() {
			manipulator.state.deleted.push(this.uri);
		}
	},
}));
vi.mock("expo-crypto", () => ({ randomUUID: () => "id" }));
vi.mock("expo-image-picker", () => ({
	launchImageLibraryAsync: async () => ({
		canceled: false,
		assets: [
			{
				uri: "file:///photo.heic",
				fileName: "IMG_0005.heic",
				mimeType: "image/heic",
				fileSize: 2_000_000,
				width: 4032,
				height: 3024,
			},
		],
	}),
	UIImagePickerPreferredAssetRepresentationMode: { Current: "current" },
}));

const { nativeImagePicker } = await import("./nativeImagePicker");

const photo = { uri: "file:///photo.heic", name: "IMG_0005.heic", type: "image/heic", size: 2_000_000 };
const screenshot = { uri: "file:///shot.png", name: "shot.png", type: "image/png", size: 900_000 };

beforeEach(() => {
	Object.assign(manipulator.state, { renders: 0, resizes: [], saves: [], live: 0, deleted: [], read: [], fail: null });
});

/** Encodes `image` whose pixels, as decoded, are `width` by `height`. */
function encodes(width: number, height: number, image: Parameters<typeof nativeImagePicker.encode>[0] = photo) {
	manipulator.state.source = { width, height };
	return nativeImagePicker.encode(image);
}

it("scales a landscape camera photo to 2048 px wide and saves it once as JPEG", async () => {
	expect(await encodes(4032, 3024)).toEqual({ data: "AQID", mediaType: "image/jpeg" });
	expect(manipulator.state.resizes).toEqual([{ width: 2048 }]);
	expect(manipulator.state.saves).toEqual([
		{ width: 2048, height: 1536, options: { format: "jpeg", compress: 0.85, base64: true } },
	]);
});

it("scales a portrait photo along its height", async () => {
	await encodes(3024, 4032);
	expect(manipulator.state.resizes).toEqual([{ height: 2048 }]);
	expect(manipulator.state.saves).toMatchObject([{ width: 1536, height: 2048 }]);
});

it("never enlarges an image already within 2048 px", async () => {
	await encodes(2048, 1000);
	await encodes(800, 600);
	expect(manipulator.state.resizes).toEqual([]);
	expect(manipulator.state.saves).toMatchObject([
		{ width: 2048, height: 1000 },
		{ width: 800, height: 600 },
	]);
});

it("decodes a photo once when the picker says its size", async () => {
	await encodes(4032, 3024, { ...photo, width: 4032, height: 3024 });
	expect(manipulator.state.renders).toBe(1);
	expect(manipulator.state.resizes).toEqual([{ width: 2048 }]);
});

it("measures a photo with one render first when the picker doesn't say its size", async () => {
	await encodes(4032, 3024, { ...photo, width: 0, height: 0 });
	expect(manipulator.state.renders).toBe(2);
	expect(manipulator.state.resizes).toEqual([{ width: 2048 }]);
});

it("sends a PNG within 2048 px and the limit as it is, so a screenshot keeps its sharpness and transparency", async () => {
	expect(await encodes(1179, 2048, { ...screenshot, width: 1179, height: 2048 })).toEqual({
		data: "UE5H",
		mediaType: "image/png",
	});
	expect(manipulator.state.read).toEqual(["file:///shot.png"]);
	expect(manipulator.state.renders).toBe(0);
	expect(manipulator.state.saves).toEqual([]);
});

it("scales a PNG over 2048 px, or over the limit, as JPEG like a photo", async () => {
	expect(await encodes(2360, 1640, { ...screenshot, width: 2360, height: 1640 })).toMatchObject({
		mediaType: "image/jpeg",
	});
	expect(
		await encodes(1179, 2048, { ...screenshot, size: MAX_ATTACHMENT_BYTES + 1, width: 1179, height: 2048 }),
	).toMatchObject({ mediaType: "image/jpeg" });
	expect(manipulator.state.read).toEqual([]);
});

it("lets every native image go and deletes its cache file", async () => {
	await encodes(4032, 3024);
	expect(manipulator.state.live).toBe(0);
	expect(manipulator.state.deleted).toEqual(["file:///cache/1.jpg"]);
});

it("lets every native image go when the manipulator fails, and says so to its caller", async () => {
	manipulator.state.fail = "render";
	await expect(encodes(4032, 3024, { ...photo, width: 4032, height: 3024 })).rejects.toThrow("could not decode");
	expect(manipulator.state.live).toBe(0);
	manipulator.state.fail = "save";
	await expect(encodes(4032, 3024, { ...photo, width: 4032, height: 3024 })).rejects.toThrow("disk full");
	expect(manipulator.state.live).toBe(0);
});

it("reports the pixel size the picker gives for each photo", async () => {
	expect(await nativeImagePicker.pick(1)).toEqual([{ ...photo, width: 4032, height: 3024 }]);
});
