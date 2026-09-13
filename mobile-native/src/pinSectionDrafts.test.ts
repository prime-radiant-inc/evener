import { describe, expect, it } from "vitest";
import { pinSectionDrafts } from "./pinSectionDrafts";

function backend() {
	const values = new Map<string, unknown>();
	let sequence = 0;
	const api = {
		values,
		fail: false,
		createId: () => `draft-${++sequence}`,
		get: (key: string) => values.get(key),
		set: (key: string, value: unknown) => {
			if (api.fail) throw Error("write failed");
			values.set(key, value);
		},
		deleteIf: (key: string, expected: unknown) => {
			if (JSON.stringify(values.get(key)) !== JSON.stringify(expected))
				return false;
			values.delete(key);
			return true;
		},
	};
	return api;
}
describe("pin section drafts", () => {
	it("restores raw names and isolates hub and section tuple scopes", () => {
		const b = backend(),
			first = pinSectionDrafts("ab", "c", b),
			second = pinSectionDrafts("a", "bc", b);
		const saved = first.save("  Focus  ");
		second.save("");
		expect(pinSectionDrafts("ab", "c", b).load()).toEqual(saved);
		expect(second.load()?.name).toBe("");
		expect(pinSectionDrafts("other", "c", b).load()).toBeNull();
		expect(pinSectionDrafts("ab", "other", b).load()).toBeNull();
	});
	it("preserves the last proposal when a write fails", () => {
		const b = backend(),
			repo = pinSectionDrafts("hub", "section", b),
			saved = repo.save("first");
		b.fail = true;
		expect(() => repo.save("second")).toThrow();
		expect(repo.load()).toEqual(saved);
	});
	it("blocks corrupt storage instead of overwriting it", () => {
		const b = backend(),
			repo = pinSectionDrafts("hub", "section", b);
		b.values.set(repo.key, { id: "bad" });
		expect(() => repo.save("replacement")).toThrow();
		expect(b.values.get(repo.key)).toEqual({ id: "bad" });
	});
	it("cannot clear a newer proposal even when the name matches", () => {
		const b = backend(),
			repo = pinSectionDrafts("hub", "section", b),
			first = repo.save("same"),
			second = repo.save("same");
		expect(repo.removeIf(first)).toBe(false);
		expect(repo.load()).toEqual(second);
		expect(repo.removeIf(second)).toBe(true);
		expect(repo.load()).toBeNull();
	});
	it("isolates returned drafts from the stored record", () => {
		const b = backend(),
			repo = pinSectionDrafts("hub", "section", b),
			saved = repo.save("original");
		saved.name = "changed";
		const loaded = repo.load();
		if (loaded) loaded.name = "changed again";
		expect(repo.load()?.name).toBe("original");
	});
});
