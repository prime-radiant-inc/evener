import { describe, expect, it } from "vitest";
import {
	type PinAssignmentDraftBackend,
	pinAssignmentDrafts,
} from "./pinAssignmentDrafts";

function backend() {
	const values = new Map<string, unknown>();
	let sequence = 0;
	const api: PinAssignmentDraftBackend & {
		values: Map<string, unknown>;
		fail: boolean;
	} = {
		values,
		fail: false,
		createId: () => `draft-${++sequence}`,
		get: (key) => structuredClone(values.get(key)),
		set: (key, value) => {
			if (api.fail) throw new Error("write failed");
			values.set(key, structuredClone(value));
		},
		deleteIf: (key, expected) => {
			if (JSON.stringify(values.get(key)) !== JSON.stringify(expected))
				return false;
			values.delete(key);
			return true;
		},
	};
	return api;
}
const existing = { kind: "existing" as const, sectionId: "focus" };
const fresh = { kind: "new" as const, name: "  New section  " };
describe("pin assignment drafts", () => {
	it("recreates a draft and scopes identical refs by hub", () => {
		const b = backend();
		const a = pinAssignmentDrafts("hub-a", "session", b);
		const other = pinAssignmentDrafts("hub-b", "session", b);
		const saved = a.save(fresh);
		expect(a.load()).toEqual(saved);
		expect(other.load()).toBeNull();
		expect(pinAssignmentDrafts("hub-a", "session", b).load()).toEqual(saved);
	});
	it("keeps scopes distinct when naive concatenation would collide", () => {
		const b = backend();
		const first = pinAssignmentDrafts("ab", "c", b);
		const second = pinAssignmentDrafts("a", "bc", b);
		const saved = first.save(existing);
		second.save({ kind: "new", name: "other" });
		expect(first.load()).toEqual(saved);
		expect(second.load()).toMatchObject({ selection: { name: "other" } });
		expect(first.key).not.toBe(second.key);
	});
	it("deep-copies selections on save, return, and load", () => {
		const b = backend();
		b.get = (key) => b.values.get(key);
		b.set = (key, value) => {
			b.values.set(key, value);
		};
		const a = pinAssignmentDrafts("hub", "session", b);
		const input = { kind: "new" as const, name: "Draft" };
		const saved = a.save(input);
		input.name = "changed after save";
		if (saved.selection?.kind === "new")
			saved.selection.name = "changed after return";
		expect(a.load()).toEqual({
			id: "draft-1",
			selection: { kind: "new", name: "Draft" },
		});
		const loaded = a.load();
		if (loaded?.selection?.kind === "new")
			loaded.selection.name = "changed after load";
		expect(a.load()).toEqual({
			id: "draft-1",
			selection: { kind: "new", name: "Draft" },
		});
	});
	it("keeps the previous draft when a write fails", () => {
		const b = backend();
		const a = pinAssignmentDrafts("hub", "session", b);
		const saved = a.save(existing);
		b.fail = true;
		expect(() => a.save(fresh)).toThrow();
		expect(a.load()).toEqual(saved);
	});
	it("rejects corruption before save can overwrite it", () => {
		const b = backend();
		const a = pinAssignmentDrafts("hub", "session", b);
		b.values.set(a.key, { broken: true });
		expect(() => a.save(fresh)).toThrow();
		expect(b.values.get(a.key)).toEqual({ broken: true });
	});
	it("conditionally clears only the exact saved generation", () => {
		const b = backend();
		const a = pinAssignmentDrafts("hub", "session", b);
		const first = a.save(fresh);
		const second = a.save(fresh);
		expect(a.removeIf(first)).toBe(false);
		expect(a.load()).toEqual(second);
		expect(a.removeIf(second)).toBe(true);
		expect(a.load()).toBeNull();
	});
	it("rejects empty existing ids but preserves empty new names", () => {
		const b = backend();
		const a = pinAssignmentDrafts("hub", "session", b);
		expect(() => a.save({ kind: "existing", sectionId: "  " })).toThrow();
		expect(a.save({ kind: "new", name: "   " }).selection).toEqual({
			kind: "new",
			name: "   ",
		});
	});
});
