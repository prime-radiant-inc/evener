import { describe, expect, it } from "vitest";
import { nativeKeybindingDrafts } from "./nativePreferenceDrafts";

// The keybindings store's discardClassified (and the settle paths that will
// adopt a checkpoint replaced under them) branch on removeIf's own boolean:
// a refusal means another writer replaced the record, and the caller must
// adopt what is actually on disk rather than assume its own removal
// succeeded. nativeKeybindingDrafts must propagate the backend's own
// deleteIf verdict faithfully, never silently reporting a refusal as if it
// had removed the record (or vice versa).
function backend() {
	const store = new Map<string, unknown>();
	return {
		store,
		createId: () => "draft-1",
		get: (key: string) => store.get(key) ?? null,
		set: (key: string, value: unknown) => {
			store.set(key, value);
		},
		delete: (key: string) => {
			store.delete(key);
		},
		deleteIf: (key: string, value: unknown): boolean => {
			if (JSON.stringify(store.get(key)) !== JSON.stringify(value)) return false;
			store.delete(key);
			return true;
		},
	};
}

const checkpoint = { id: "draft-1", baseRevision: 3, rules: [], writeUncertain: false };

describe("nativeKeybindingDrafts", () => {
	it("propagates a refused deleteIf as false through removeIf, leaving the stored record intact", () => {
		const b = backend();
		b.store.set("evener.native.keybinding-draft.hub", { ...checkpoint, id: "someone-else" });
		const storage = nativeKeybindingDrafts("hub", b);

		expect(storage.removeIf(checkpoint)).toBe(false);
		expect(b.store.get("evener.native.keybinding-draft.hub")).toEqual({ ...checkpoint, id: "someone-else" });
	});

	it("propagates a successful deleteIf as true through removeIf, clearing the stored record", () => {
		const b = backend();
		b.store.set("evener.native.keybinding-draft.hub", checkpoint);
		const storage = nativeKeybindingDrafts("hub", b);

		expect(storage.removeIf(checkpoint)).toBe(true);
		expect(b.store.has("evener.native.keybinding-draft.hub")).toBe(false);
	});
});
