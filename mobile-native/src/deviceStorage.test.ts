import { expect, it } from "vitest";
import { readJson, removeKeys, writeJson } from "./deviceStorage";
import type { SyncStringStorage } from "./syncStringStorage";
import { memoryStorage } from "./syncStringStorageTestUtils";

const broken: SyncStringStorage = {
	getItemSync: () => {
		throw new Error("disk");
	},
	setItemSync: () => {
		throw new Error("disk");
	},
	removeItemSync: () => {
		throw new Error("disk");
	},
};

it("reads JSON back, and reads missing, corrupt or unreadable records as null", () => {
	const storage = memoryStorage(
		new Map([
			["good", '{"a":1}'],
			["bad", "{not json"],
		]),
	);
	expect(readJson(storage, "good")).toEqual({ a: 1 });
	expect(readJson(storage, "bad")).toBeNull();
	expect(readJson(storage, "absent")).toBeNull();
	expect(readJson(broken, "good")).toBeNull();
});

it("writes JSON and keeps going when the store refuses", () => {
	const storage = memoryStorage();
	writeJson(storage, "k", { b: [1, 2] });
	expect(storage.values.get("k")).toBe('{"b":[1,2]}');
	expect(() => writeJson(broken, "k", {})).not.toThrow();
});

it("removes every key it names, without throwing", () => {
	const storage = memoryStorage(
		new Map([
			["a", "1"],
			["b", "2"],
			["c", "3"],
		]),
	);
	expect(() => removeKeys(storage, ["a", "c"])).not.toThrow();
	expect([...storage.values.keys()]).toEqual(["b"]);
});

it("still removes the other keys when one's removal fails, then throws", () => {
	const removed: string[] = [];
	const storage: SyncStringStorage = {
		getItemSync: () => null,
		setItemSync: () => {},
		removeItemSync: (key) => {
			if (key === "a") throw new Error("disk");
			removed.push(key);
		},
	};
	expect(() => removeKeys(storage, ["a", "b"])).toThrow();
	expect(removed).toEqual(["b"]);
});
