import { expect, it, vi } from "vitest";
import { appGroupSharePort, SHARE_APP_GROUP, type ShareGroupDirectory } from "./appGroupSharePort";

// expo-file-system re-exports through react-native's Flow sources, which
// vitest cannot parse; every suite that reaches it mocks it.
vi.mock("expo-file-system", () => ({ Paths: { appleSharedContainers: {} } }));

function file(name: string, text: string) {
	return {
		name,
		text: async () => text,
		textSync: () => text,
		delete: vi.fn(),
		exists: true,
	};
}

/** A fake App Group directory that records deletions. */
function fakeGroup(entries: Array<ReturnType<typeof file>>) {
	const listed = entries.map((entry) => ({
		...entry,
		name: entry.name,
	}));
	return {
		listed,
		directory: {
			exists: true,
			list: vi.fn(() => listed),
			file: (name: string) => listed.find((entry) => entry.name === name) ?? null,
		} as unknown as ShareGroupDirectory,
	};
}

it("lists only share files from the app group directory", async () => {
	const share = file("share-a.json", "{}");
	const other = file("notes.txt", "hi");
	const { directory } = fakeGroup([share, other]);
	const port = appGroupSharePort(() => directory);
	expect(await port.list()).toEqual(["share-a.json"]);
});

it("reads a share's text from the directory", async () => {
	const share = file("share-a.json", '{"id":"a"}');
	const { directory } = fakeGroup([share]);
	const port = appGroupSharePort(() => directory);
	expect(await port.read("share-a.json")).toBe('{"id":"a"}');
	// The read resolves its file without enumerating the directory again.
	expect(directory.list).toHaveBeenCalledTimes(0);
});

it("removes only the file it names", async () => {
	const share = file("share-a.json", "{}");
	const other = file("share-b.json", "{}");
	const { directory } = fakeGroup([share, other]);
	const port = appGroupSharePort(() => directory);
	await port.remove("share-a.json");
	expect(share.delete).toHaveBeenCalledTimes(1);
	expect(other.delete).not.toHaveBeenCalled();
});

it("reports no files when the app group container is absent", async () => {
	const port = appGroupSharePort(() => null);
	expect(await port.list()).toEqual([]);
	await expect(port.remove("share-a.json")).resolves.toBeUndefined();
});

it("refuses a container that does not exist", async () => {
	const port = appGroupSharePort(() => ({ exists: false, list: () => [] }) as unknown as ShareGroupDirectory);
	expect(await port.list()).toEqual([]);
});

it("names the app group the extension writes to", () => {
	expect(SHARE_APP_GROUP).toBe("group.com.primeradiant.evener.native");
});
