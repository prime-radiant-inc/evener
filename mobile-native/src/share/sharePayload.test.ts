import { expect, it } from "vitest";
import { completeShare, pendingShares, parseSharePayload, type SharePort, shareFileName } from "./sharePayload";

const HOUR = 60 * 60 * 1000;

function payload(id: string, text: string, createdAt: string): { name: string; raw: string } {
	return {
		name: shareFileName(id),
		raw: JSON.stringify({ id, text, createdAt }),
	};
}

/** A directory of share files, addressed by file name. */
function fakePort(files: Record<string, string>): SharePort & { removed: string[] } {
	const removed: string[] = [];
	return {
		removed,
		async list() {
			return Object.keys(files).filter((name) => !removed.includes(name));
		},
		async read(name) {
			if (!(name in files)) throw new Error(`no such file: ${name}`);
			return files[name];
		},
		async remove(name) {
			removed.push(name);
			delete files[name];
		},
	};
}

it("reads a payload whose file name matches its id", () => {
	const { name, raw } = payload("a1", "Ship it", "2026-10-18T20:00:00.000Z");
	const parsed = parseSharePayload(name, raw);
	expect(parsed).toEqual({ id: "a1", text: "Ship it", createdAt: "2026-10-18T20:00:00.000Z" });
});

it("refuses a payload whose id and file name disagree", () => {
	const { raw } = payload("a1", "Ship it", "2026-10-18T20:00:00.000Z");
	expect(shareFileName("a1")).toBe("share-a1.json");
	expect(parseSharePayload("share-other.json", raw)).toBeNull();
});

it("refuses a file that is not a payload at all", () => {
	expect(parseSharePayload("share-a1.json", "not json")).toBeNull();
	expect(parseSharePayload("share-a1.json", JSON.stringify({ id: "a1", text: "   ", createdAt: "x" }))).toBeNull();
	expect(parseSharePayload("share-a1.json", JSON.stringify({ id: "a1", text: "x", createdAt: 12 }))).toBeNull();
});

it("returns pending shares oldest first, skipping files it cannot read", async () => {
	const older = payload("old", "Older", new Date(2 * HOUR).toISOString());
	const newer = payload("new", "Newer", new Date(3 * HOUR).toISOString());
	const port = fakePort({ [newer.name]: newer.raw, [older.name]: older.raw, "share-broken.json": "{}" });
	const shares = await pendingShares(port);
	expect(shares.map((share) => share.id)).toEqual(["old", "new"]);
	expect(port.removed).toEqual([]);
});

it("returns an empty list when nothing was shared", async () => {
	expect(await pendingShares(fakePort({}))).toEqual([]);
});

it("removes exactly the share it is asked to complete", async () => {
	const one = payload("one", "One", new Date(1).toISOString());
	const two = payload("two", "Two", new Date(2).toISOString());
	const port = fakePort({ [one.name]: one.raw, [two.name]: two.raw });
	await completeShare(port, "one");
	expect(port.removed).toEqual(["share-one.json"]);
	expect((await pendingShares(port)).map((share) => share.id)).toEqual(["two"]);
});
