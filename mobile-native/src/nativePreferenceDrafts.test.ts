import { describe, expect, it } from "vitest";
import { fakeDraftBackend } from "./draftBackend.testkit";
import { nativeKeybindingDrafts } from "./nativePreferenceDrafts";

// The keybindings store's discardClassified and its settle paths (a save,
// a discard or a rebase adopting a checkpoint replaced under them) branch on
// removeIf/replaceIf's own boolean: a refusal means another writer replaced
// the record, and the caller must adopt what is actually on disk rather than
// assume its own write succeeded. nativeKeybindingDrafts must propagate the
// backend's own deleteIf/replaceIf verdict faithfully, never silently
// reporting a refusal as if it had written the record (or vice versa).
const checkpoint = { id: "draft-1", baseRevision: 3, rules: [], writeUncertain: false };

describe("nativeKeybindingDrafts", () => {
	it("propagates a refused deleteIf as false through removeIf, leaving the stored record intact", () => {
		const b = fakeDraftBackend();
		b.store.set("evener.native.keybinding-draft.hub", { ...checkpoint, id: "someone-else" });
		const storage = nativeKeybindingDrafts("hub", b);

		expect(storage.removeIf(checkpoint)).toBe(false);
		expect(b.store.get("evener.native.keybinding-draft.hub")).toEqual({ ...checkpoint, id: "someone-else" });
	});

	it("propagates a successful deleteIf as true through removeIf, clearing the stored record", () => {
		const b = fakeDraftBackend();
		b.store.set("evener.native.keybinding-draft.hub", checkpoint);
		const storage = nativeKeybindingDrafts("hub", b);

		expect(storage.removeIf(checkpoint)).toBe(true);
		expect(b.store.has("evener.native.keybinding-draft.hub")).toBe(false);
	});

	it("propagates a refused insertIfAbsent as false through insertIfAbsent, leaving the stored record intact", () => {
		const b = fakeDraftBackend();
		const someoneElse = { ...checkpoint, id: "someone-else" };
		b.store.set("evener.native.keybinding-draft.hub", someoneElse);
		const storage = nativeKeybindingDrafts("hub", b);

		expect(storage.insertIfAbsent(checkpoint)).toBe(false);
		expect(b.store.get("evener.native.keybinding-draft.hub")).toEqual(someoneElse);
	});

	it("propagates a successful insertIfAbsent as true through insertIfAbsent, writing the new record", () => {
		const b = fakeDraftBackend();
		const storage = nativeKeybindingDrafts("hub", b);

		expect(storage.insertIfAbsent(checkpoint)).toBe(true);
		expect(b.store.get("evener.native.keybinding-draft.hub")).toEqual(checkpoint);
	});

	it("propagates a refused replaceIf as false through replaceIf, leaving the stored record intact", () => {
		const b = fakeDraftBackend();
		const someoneElse = { ...checkpoint, id: "someone-else" };
		b.store.set("evener.native.keybinding-draft.hub", someoneElse);
		const storage = nativeKeybindingDrafts("hub", b);

		expect(storage.replaceIf(checkpoint, { ...checkpoint, writeUncertain: false })).toBe(false);
		expect(b.store.get("evener.native.keybinding-draft.hub")).toEqual(someoneElse);
	});

	it("propagates a successful replaceIf as true through replaceIf, writing the new record", () => {
		const b = fakeDraftBackend();
		b.store.set("evener.native.keybinding-draft.hub", checkpoint);
		const storage = nativeKeybindingDrafts("hub", b);
		const next = { ...checkpoint, writeUncertain: false };

		expect(storage.replaceIf(checkpoint, next)).toBe(true);
		expect(b.store.get("evener.native.keybinding-draft.hub")).toEqual(next);
	});
});
