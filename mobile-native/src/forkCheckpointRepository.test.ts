import { describe, expect, it } from "vitest";
import { forkCheckpoints } from "./forkCheckpointRepository";

function disk() {
	const values = new Map<string, unknown>();
	let next = 0;
	return {
		values,
		fail: false,
		createId: () => `fork-${++next}`,
		get: (key: string) => values.get(key),
		set(key: string, value: unknown) {
			if (this.fail) throw Error("disk unavailable");
			values.set(key, value);
		},
		deleteIf(key: string, expected: unknown) {
			if (this.fail) throw Error("disk unavailable");
			if (JSON.stringify(values.get(key)) !== JSON.stringify(expected))
				return false;
			return values.delete(key);
		},
	};
}
const target = {
	instanceId: "parent-instance",
	entryIndex: 5,
	preview: "  selected input  ",
};
const child = { ref: "child", title: "Fork", input: "original input" };
describe("native fork checkpoints", () => {
	it("restores isolated targets and rejects overlapping requests", () => {
		const backend = disk(),
			repo = forkCheckpoints("hub", "parent", backend);
		const saved = repo.begin(target);
		expect(forkCheckpoints("hub", "parent", backend).load()).toEqual(saved);
		expect(forkCheckpoints("other", "parent", backend).load()).toBeNull();
		expect(forkCheckpoints("hub", "other", backend).load()).toBeNull();
		expect(() => repo.begin({ ...target, entryIndex: 9 })).toThrow();
		saved.target.preview = "mutated outside storage";
		expect(repo.load()?.target).toEqual(target);
	});
	it("keeps a pending request when acknowledgement storage fails and can retry the acknowledgement", () => {
		const backend = disk(),
			repo = forkCheckpoints("hub", "parent", backend);
		const saved = repo.begin(target);
		backend.fail = true;
		expect(() => repo.acknowledge(saved, child)).toThrow();
		expect(repo.load()).toEqual(saved);
		backend.fail = false;
		const accepted = repo.acknowledge(saved, child);
		expect(repo.load()?.child).toEqual(child);
		expect(repo.removeIf(saved)).toBe(false);
		const prepared = repo.prepareDraft(accepted);
		expect(prepared.draftPrepared).toBe(true);
		expect(repo.removeIf(accepted)).toBe(false);
		expect(repo.removeIf(prepared)).toBe(true);
	});
	it("does not resurrect removed or replacement checkpoints through a late acknowledgement", () => {
		const backend = disk(),
			repo = forkCheckpoints("hub", "parent", backend);
		const first = repo.begin(target);
		repo.removeIf(first);
		expect(() => repo.acknowledge(first, child)).toThrow();
		expect(repo.load()).toBeNull();
		const second = repo.begin({ ...target, entryIndex: 9 });
		expect(() => repo.acknowledge(first, child)).toThrow();
		expect(repo.removeIf(first)).toBe(false);
		expect(repo.load()).toEqual(second);
	});
	it("refuses invalid targets, parent-as-child and unacknowledged draft preparation", () => {
		const backend = disk(),
			repo = forkCheckpoints("hub", "parent", backend);
		for (const entryIndex of [
			0,
			-1,
			1.5,
			Infinity,
			Number.MAX_SAFE_INTEGER + 1,
		])
			expect(() => repo.begin({ ...target, entryIndex })).toThrow();
		expect(repo.load()).toBeNull();
		const saved = repo.begin(target);
		expect(() =>
			repo.acknowledge(saved, { ...child, ref: "parent" }),
		).toThrow();
		expect(() => repo.prepareDraft(saved)).toThrow();
		expect(repo.load()).toEqual(saved);
	});
	it("does not overwrite corrupt storage or create a request when storage fails", () => {
		const backend = disk(),
			repo = forkCheckpoints("hub", "parent", backend);
		backend.fail = true;
		expect(() => repo.begin(target)).toThrow();
		expect(repo.load()).toBeNull();
		backend.fail = false;
		const saved = repo.begin(target);
		backend.values.set(repo.key, {
			...saved,
			target: { ...target, entryIndex: 0 },
		});
		expect(() => repo.load()).toThrow();
		expect(() => repo.begin(target)).toThrow();
	});
});
