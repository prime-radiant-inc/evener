import { beforeEach, expect, it, vi } from "vitest";

// A stand-in for expo-image-manipulator that records what the picker asks of
// it: each context's resize, each save's options, and every native object it
// lets go of.
const manipulator = vi.hoisted(() => {
	const state = {
		source: { width: 0, height: 0 },
		resizes: [] as unknown[],
		saves: [] as unknown[],
		live: 0,
		deleted: [] as string[],
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
					state.live++;
					return {
						...size,
						async saveAsync(options: unknown) {
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

const picked = { uri: "file:///photo.heic", name: "IMG_0005.heic", type: "image/heic", size: 2_000_000 };

beforeEach(() => {
	Object.assign(manipulator.state, { resizes: [], saves: [], live: 0, deleted: [] });
});

function encodes(width: number, height: number) {
	manipulator.state.source = { width, height };
	return nativeImagePicker.encode(picked);
}

it("scales a landscape camera photo to 2048 px wide and saves it once as JPEG", async () => {
	expect(await encodes(4032, 3024)).toBe("AQID");
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

it("lets every native image go and deletes its cache file", async () => {
	await encodes(4032, 3024);
	expect(manipulator.state.live).toBe(0);
	expect(manipulator.state.deleted).toEqual(["file:///cache/1.jpg"]);
});

it("reports the pixel size the picker gives for each photo", async () => {
	expect(await nativeImagePicker.pick(1)).toEqual([{ ...picked, width: 4032, height: 3024 }]);
});
